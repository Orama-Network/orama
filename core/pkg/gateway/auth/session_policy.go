package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/client"
)

// What a namespace requires of a session, per namespace and opt-in.
//
// A client that binds a device gets a device-bound session in every
// namespace; binding one only ever narrows what a stolen credential can do.
// The policy is about what a sign-in that does not bind one may still get.

// DevicePolicy is a namespace's rule for sessions.
type DevicePolicy string

const (
	// DevicePolicyOptional binds a device when the client sends one. It is
	// what a namespace with no policy has, and how every namespace worked
	// before devices existed.
	DevicePolicyOptional DevicePolicy = "optional"
	// DevicePolicyRequired refuses an end-user sign-in that binds no device.
	DevicePolicyRequired DevicePolicy = "required"
	// DevicePolicyApproval is DevicePolicyRequired, and a device beyond an
	// account's first starts pending: a wallet signature alone enrols it but
	// gives it no session until one of the account's active devices approves.
	DevicePolicyApproval DevicePolicy = "approval"
	// DevicePolicyFunction is DevicePolicyRequired, and a device beyond an
	// account's first starts pending with a restricted session: it reaches
	// only the functions deployed open to pending devices, its own session and
	// its own attestation, until one of the namespace's functions approves it
	// (approve_device). The namespace runs its own admission — a join request
	// answered from another device, a recovery check — and the gateway holds
	// the device to it.
	DevicePolicyFunction DevicePolicy = "function"
)

// devicePolicyStaleness is how long a refresh may be judged by a policy a
// gateway read. Every account-level refresh asks, so refresh reads through a
// cache; the bound is the revocation list's, the other thing a gateway may be
// that far behind on. Paths that issue a credential — sign-in, API keys,
// approvals, device-link claims — read the policy itself: they are rare, and
// a credential minted in the stale window would outlive it.
const devicePolicyStaleness = RevocationRefreshInterval

// ErrDeviceRequired is an end-user sign-in without a device, in a namespace
// that requires one.
var ErrDeviceRequired = errors.New("this namespace requires sessions bound to a device: sign in with a device key")

// ParseDevicePolicy reads a policy name.
func ParseDevicePolicy(raw string) (DevicePolicy, error) {
	switch p := DevicePolicy(strings.ToLower(strings.TrimSpace(raw))); p {
	case DevicePolicyOptional, DevicePolicyRequired, DevicePolicyApproval, DevicePolicyFunction:
		return p, nil
	default:
		return "", fmt.Errorf("device policy is one of optional, required, approval, function (got %q)", raw)
	}
}

// DevicePolicyOf returns a namespace's policy, as recorded now.
func (s *Service) DevicePolicyOf(ctx context.Context, namespace string) (DevicePolicy, error) {
	namespace = sessionNamespace(namespace)
	db, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return "", err
	}
	res, err := db.Query(client.WithInternalAuth(ctx),
		`SELECT device_policy FROM namespace_session_policy WHERE namespace_id = ? LIMIT 1`, nsID)
	if err != nil {
		return "", fmt.Errorf("read the session policy of %q: %w", namespace, err)
	}
	policy := DevicePolicyOptional
	if res != nil && len(res.Rows) > 0 && len(res.Rows[0]) > 0 {
		if policy, err = ParseDevicePolicy(getStringVal(res.Rows[0][0])); err != nil {
			return "", err
		}
	}
	s.devicePolicies.put(namespace, policy, time.Now())
	return policy, nil
}

// cachedDevicePolicyOf is DevicePolicyOf, answered from what this gateway read
// within devicePolicyStaleness.
func (s *Service) cachedDevicePolicyOf(ctx context.Context, namespace string) (DevicePolicy, error) {
	namespace = sessionNamespace(namespace)
	if p, ok := s.devicePolicies.get(namespace, time.Now()); ok {
		return p, nil
	}
	return s.DevicePolicyOf(ctx, namespace)
}

// SetDevicePolicy records a namespace's policy, and reports how many end-user
// sign-in keys it revoked.
//
// A key minted by an end user's sign-in is a credential bound to no device.
// Left alone, every member who signed in before the policy would keep one —
// minting access tokens through /v1/auth/token for up to ninety days — and the
// policy would hold for nobody who had already been there. Requiring devices
// therefore revokes them; the operators' own keys are untouched.
func (s *Service) SetDevicePolicy(ctx context.Context, namespace string, policy DevicePolicy, actor string) (int, error) {
	if _, err := ParseDevicePolicy(string(policy)); err != nil {
		return 0, err
	}
	if s.db == nil {
		return 0, ErrRotationNotConfigured
	}
	namespace = sessionNamespace(namespace)
	_, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(client.WithInternalAuth(ctx),
		`INSERT INTO namespace_session_policy(namespace_id, device_policy, updated_by, updated_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(namespace_id) DO UPDATE SET
		   device_policy = excluded.device_policy,
		   updated_by = excluded.updated_by,
		   updated_at = excluded.updated_at`,
		nsID, string(policy), actor); err != nil {
		return 0, fmt.Errorf("record the session policy of %q: %w", namespace, err)
	}
	s.devicePolicies.put(namespace, policy, time.Now())
	if policy == DevicePolicyOptional {
		return 0, nil
	}
	revoked, err := s.revokeEndUserSignInKeys(ctx, namespace)
	if err != nil {
		return revoked, fmt.Errorf("%w (%d revoked before it stopped): %w", ErrSignInKeySweepIncomplete, revoked, err)
	}
	return revoked, nil
}

// DevicePolicyFor is the policy a wallet's sign-in is held to, as recorded now.
//
// A wallet holding a control-plane role — owner, admin, developer — is held to
// DevicePolicyOptional whatever the namespace says. The policy protects the
// application's users; the people who operate the namespace sign in from the
// CLI, which holds no device key, and a policy that locked them out would lock
// out the only people able to change it. Nothing is given away by it: whoever
// holds such a wallet can change the policy.
func (s *Service) DevicePolicyFor(ctx context.Context, namespace, wallet string) (DevicePolicy, error) {
	return s.devicePolicyFor(ctx, namespace, wallet, s.DevicePolicyOf)
}

// refreshDevicePolicyFor is DevicePolicyFor for a refresh, which may be judged
// by a policy up to devicePolicyStaleness old.
func (s *Service) refreshDevicePolicyFor(ctx context.Context, namespace, wallet string) (DevicePolicy, error) {
	return s.devicePolicyFor(ctx, namespace, wallet, s.cachedDevicePolicyOf)
}

func (s *Service) devicePolicyFor(ctx context.Context, namespace, wallet string, policyOf func(context.Context, string) (DevicePolicy, error)) (DevicePolicy, error) {
	namespace = sessionNamespace(namespace)
	if IsLobbyNamespace(namespace) {
		return DevicePolicyOptional, nil
	}
	policy, err := policyOf(ctx, namespace)
	if err != nil || policy == DevicePolicyOptional {
		return policy, err
	}
	operator, err := s.holdsControlPlaneRole(ctx, namespace, wallet)
	if err != nil {
		return "", err
	}
	if operator {
		return DevicePolicyOptional, nil
	}
	return policy, nil
}

// holdsControlPlaneRole reports whether a wallet's grant in a namespace is one
// that operates it.
func (s *Service) holdsControlPlaneRole(ctx context.Context, namespace, wallet string) (bool, error) {
	if s.keyORM() == nil {
		return false, fmt.Errorf("client not initialized")
	}
	nsID, err := s.resolveKeyNamespaceID(ctx, namespace)
	if err != nil {
		return false, fmt.Errorf("resolve the namespace %q: %w", namespace, err)
	}
	grant, err := s.GrantIn(ctx, s.keyORM().Database(), nsID, PrincipalWallet, NormalizeWallet(wallet))
	if errors.Is(err, ErrNotAMember) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the grant of %s in %q: %w", RedactSubject(wallet), namespace, err)
	}
	return isControlPlaneRole(grant.Role), nil
}

func isControlPlaneRole(role Role) bool {
	switch role {
	case RoleOwner, RoleAdmin, RoleDeveloper:
		return true
	}
	return false
}
