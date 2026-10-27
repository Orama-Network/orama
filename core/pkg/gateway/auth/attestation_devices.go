package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/attest"
	"github.com/Orama-Network/orama/core/pkg/attest/refusal"
	"github.com/Orama-Network/orama/core/pkg/client"
	"github.com/Orama-Network/orama/core/pkg/logging"
	"go.uber.org/zap"
)

// A device attests in three steps: it asks for a challenge, has the platform
// sign over the challenge and its own device id, and posts the proof. The
// gateway checks the proof with package attest and records what it proved,
// keyed (namespace, device): the device the session is bound to, so a proof
// made on one device or install is worthless on another.
//
// Challenges live in the nonces table beside the sign-in challenges and the
// device proofs: Raft-replicated, so a challenge issued by one gateway is
// consumed on any other, and consumed by one conditional UPDATE whose
// affected-row count is the lock, so it is used exactly once. The nonce reaper
// removes them.

const (
	// AttestationChallengeTTL is how long a challenge may be answered: long
	// enough for a phone to reach the platform's attestation service and back.
	AttestationChallengeTTL = 5 * time.Minute

	// attestationChallengeBytes is the randomness in a challenge.
	attestationChallengeBytes = 32

	// attestationNoncePrefix files a device's challenges in the nonces table
	// under a name no wallet can have, and attestationNoncePurpose says what
	// they are for.
	attestationNoncePrefix  = "attest:"
	attestationNoncePurpose = "attestation"
)

var (
	// ErrAttestationNotConfigured is an attestation sent to a namespace whose
	// mode is off: there is no app to attest against.
	ErrAttestationNotConfigured = errors.New("this namespace does not attest devices; its owner sets an attestation policy first")

	// ErrAttestationNeedsDevice is an attestation, or a challenge, asked for by
	// a session bound to no device. A proof is bound to the device's id.
	ErrAttestationNeedsDevice = errors.New("attestation is bound to a device: sign in with a device key")

	// ErrAttestationVerifierMissing is a gateway built without a verifier.
	ErrAttestationVerifierMissing = errors.New("this gateway cannot verify attestations")
)

// AttestationBinding is the value a device's platform signs over: the
// SHA-256 of the challenge followed by the device id. On iOS it is the
// attestation's and the assertion's clientDataHash, on Android the key's
// attestationChallenge. The device id is a fixed-length thumbprint, so the
// concatenation is unambiguous.
func AttestationBinding(challenge, deviceID string) [32]byte {
	return sha256.Sum256([]byte(challenge + deviceID))
}

// SetAttestationVerifier gives the service what checks attestation proofs.
func (s *Service) SetAttestationVerifier(v attest.Verifier) { s.attestVerifier = v }

// IssueAttestationChallenge records a single-use challenge for a device and
// returns it with its expiry.
func (s *Service) IssueAttestationChallenge(ctx context.Context, namespace, deviceID string) (string, time.Time, error) {
	if strings.TrimSpace(deviceID) == "" {
		return "", time.Time{}, ErrAttestationNeedsDevice
	}
	db, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return "", time.Time{}, err
	}
	internalCtx := client.WithInternalAuth(ctx)
	// Nothing about a challenge is secret, but each is a replicated row: a
	// device holds a handful at most, as a wallet does at sign-in.
	if err := s.checkOutstandingNonces(internalCtx, db, nsID, attestationNoncePrefix+deviceID, namespace); err != nil {
		return "", time.Time{}, err
	}
	buf := make([]byte, attestationChallengeBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, fmt.Errorf("generate the challenge: %w", err)
	}
	challenge := base64.RawURLEncoding.EncodeToString(buf)
	expires := time.Now().Add(AttestationChallengeTTL).UTC()
	if _, err := db.Query(internalCtx,
		`INSERT INTO nonces(namespace_id, wallet, nonce, purpose, expires_at) VALUES (?, ?, ?, ?, ?)`,
		nsID, attestationNoncePrefix+deviceID, challenge, attestationNoncePurpose, expires.Format(sqliteTime)); err != nil {
		return "", time.Time{}, fmt.Errorf("store the challenge: %w", err)
	}
	return challenge, expires, nil
}

// consumeAttestationChallenge claims a device's challenge exactly once.
// ErrNonceInvalid is a challenge never issued to this device, already used or
// expired; ErrNonceTransient a registry that did not answer.
func (s *Service) consumeAttestationChallenge(ctx context.Context, nsID interface{}, deviceID, challenge string) error {
	if s.db == nil {
		return ErrNonceConsumeNotConfigured
	}
	if strings.TrimSpace(challenge) == "" {
		return ErrNonceInvalid
	}
	res, err := s.db.Exec(client.WithInternalAuth(ctx),
		`UPDATE nonces SET used_at = datetime('now')
		  WHERE namespace_id = ? AND wallet = ? AND nonce = ? AND purpose = ?
		    AND used_at IS NULL AND expires_at > datetime('now')`,
		nsID, attestationNoncePrefix+deviceID, challenge, attestationNoncePurpose)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNonceTransient, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: rows affected: %v", ErrNonceTransient, err)
	}
	if affected == 0 {
		return ErrNonceInvalid
	}
	return nil
}

// AttestIOS verifies an App Attest attestation made over the challenge and
// the device id, and records the key for the device's later assertions.
func (s *Service) AttestIOS(ctx context.Context, namespace, deviceID, challenge string, keyID, attestationObject []byte) (*AttestationClaim, error) {
	policy, nsID, err := s.attestationTarget(ctx, namespace, deviceID, attest.PlatformIOS)
	if err != nil {
		return nil, err
	}
	if err := s.consumeAttestationChallenge(ctx, nsID, deviceID, challenge); err != nil {
		return nil, err
	}
	verdict, err := s.attestVerifier.AttestIOS(policy.Verifier(), keyID, attestationObject, AttestationBinding(challenge, deviceID))
	if err != nil {
		return nil, err
	}
	record := attestationRecord{
		platform:    attest.PlatformIOS,
		appID:       verdict.AppID,
		appVersion:  verdict.AppVersion,
		development: verdict.Development,
		iosKeyID:    base64.StdEncoding.EncodeToString(verdict.KeyID),
		iosPublic:   base64.StdEncoding.EncodeToString(verdict.PublicKey),
		iosCounter:  verdict.Counter,
		attestedAt:  time.Now().UTC(),
	}
	if err := s.storeAttestation(ctx, nsID, deviceID, record); err != nil {
		return nil, err
	}
	return record.claim(), nil
}

// AttestAndroid verifies a key-attestation chain whose challenge is the
// binding of the challenge and the device id, and records what it proved.
func (s *Service) AttestAndroid(ctx context.Context, namespace, deviceID, challenge string, chain [][]byte) (*AttestationClaim, error) {
	policy, nsID, err := s.attestationTarget(ctx, namespace, deviceID, attest.PlatformAndroid)
	if err != nil {
		return nil, err
	}
	if err := s.consumeAttestationChallenge(ctx, nsID, deviceID, challenge); err != nil {
		return nil, err
	}
	binding := AttestationBinding(challenge, deviceID)
	verdict, err := s.attestVerifier.AttestAndroid(policy.Verifier(), chain, binding[:])
	if err != nil {
		return nil, err
	}
	// The verifier accepts GrapheneOS whenever its signed list carries the
	// boot key; whether this namespace does is its policy.
	if verdict.GrapheneOS && !policy.Android.AllowGrapheneOS {
		return nil, refusal.Invalid(refusal.ReasonBootKey, "this namespace does not admit GrapheneOS")
	}
	record := attestationRecord{
		platform:   attest.PlatformAndroid,
		appID:      verdict.AppID,
		appVersion: verdict.AppVersion,
		bootKey:    fmt.Sprintf("%x", verdict.BootKey),
		grapheneOS: verdict.GrapheneOS,
		// Kept so the policy as it is later judges them again: a digest
		// removed from it, or a narrower patch window, reaches this device
		// before it next attests.
		signingDigests: verdict.SigningCertDigests,
		osPatchLevel:   verdict.OSPatchLevel,
		attestedAt:     time.Now().UTC(),
	}
	if err := s.storeAttestation(ctx, nsID, deviceID, record); err != nil {
		return nil, err
	}
	return record.claim(), nil
}

// attestationTarget is the policy and namespace id an attestation from a
// device on platform is checked against, read now: attesting is rare, and a
// proof checked against a stale policy would outlive it.
func (s *Service) attestationTarget(ctx context.Context, namespace, deviceID string, platform attest.Platform) (AttestationPolicy, interface{}, error) {
	if strings.TrimSpace(deviceID) == "" {
		return AttestationPolicy{}, nil, ErrAttestationNeedsDevice
	}
	if s.attestVerifier == nil {
		return AttestationPolicy{}, nil, ErrAttestationVerifierMissing
	}
	policy, err := s.AttestationPolicyOf(ctx, namespace)
	if err != nil {
		return AttestationPolicy{}, nil, err
	}
	if policy.Mode == AttestationOff {
		return AttestationPolicy{}, nil, ErrAttestationNotConfigured
	}
	accepts := policy.AcceptsIOS()
	if platform == attest.PlatformAndroid {
		accepts = policy.AcceptsAndroid()
	}
	if !accepts {
		return AttestationPolicy{}, nil, refusal.Invalid(refusal.ReasonAppID, "this namespace admits no %s build", platform)
	}
	_, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return AttestationPolicy{}, nil, err
	}
	return policy, nsID, nil
}

// attestationRecord is one device_attestations row.
type attestationRecord struct {
	platform    attest.Platform
	appID       string
	appVersion  string
	development bool
	bootKey     string
	grapheneOS  bool
	iosKeyID    string
	iosPublic   string
	iosCounter  uint32
	// signingDigests and osPatchLevel are what an Android attestation proved
	// that the policy can narrow afterwards; a row from before migration 094
	// has neither.
	signingDigests []string
	osPatchLevel   int
	attestedAt     time.Time
}

func (r attestationRecord) claim() *AttestationClaim {
	return &AttestationClaim{
		Platform:           string(r.platform),
		AppID:              r.appID,
		AppVersion:         r.appVersion,
		Development:        r.development,
		GrapheneOS:         r.grapheneOS,
		SigningCertDigests: r.signingDigests,
		OSPatchLevel:       r.osPatchLevel,
		AttestedAt:         r.attestedAt.Unix(),
	}
}

// storeAttestation records a device's attestation over its previous one.
//
// An Android device's verified-boot key is pinned on its first attestation,
// as GrapheneOS's Auditor pins it: a later attestation from the same device
// booting an OS signed with another key is refused. The pin is the upsert's
// own condition, so two attestations racing cannot both set it.
func (s *Service) storeAttestation(ctx context.Context, nsID interface{}, deviceID string, r attestationRecord) error {
	digests := r.signingDigests
	if digests == nil {
		digests = []string{}
	}
	digestsJSON, err := json.Marshal(digests)
	if err != nil {
		return fmt.Errorf("encode the attested signing-certificate digests: %w", err)
	}
	res, err := s.db.Exec(client.WithInternalAuth(ctx),
		`INSERT INTO device_attestations(namespace_id, device_id, platform, app_id, app_version, development,
		   verified_boot_key, grapheneos, ios_key_id, ios_public_key, ios_counter,
		   signing_cert_digests, os_patch_level, attested_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(namespace_id, device_id) DO UPDATE SET
		   platform = excluded.platform,
		   app_id = excluded.app_id,
		   app_version = excluded.app_version,
		   development = excluded.development,
		   verified_boot_key = excluded.verified_boot_key,
		   grapheneos = excluded.grapheneos,
		   ios_key_id = excluded.ios_key_id,
		   ios_public_key = excluded.ios_public_key,
		   ios_counter = excluded.ios_counter,
		   signing_cert_digests = excluded.signing_cert_digests,
		   os_patch_level = excluded.os_patch_level,
		   attested_at = excluded.attested_at
		 WHERE device_attestations.verified_boot_key = ''
		    OR device_attestations.verified_boot_key = excluded.verified_boot_key`,
		nsID, deviceID, string(r.platform), r.appID, r.appVersion, boolInt(r.development),
		r.bootKey, boolInt(r.grapheneOS), r.iosKeyID, r.iosPublic, int64(r.iosCounter),
		string(digestsJSON), r.osPatchLevel, r.attestedAt.Format(sqliteTime))
	if err != nil {
		return fmt.Errorf("record the attestation: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("record the attestation: rows affected: %w", err)
	}
	if affected == 0 {
		return refusal.Invalid(refusal.ReasonBootKey,
			"this device first attested booting an OS signed with another key; its verified-boot key is pinned")
	}
	return nil
}

// readAttestation is a device's record, or ok false when it has none.
func (s *Service) readAttestation(ctx context.Context, nsID interface{}, deviceID string) (attestationRecord, bool, error) {
	db := s.registryDatabase()
	if db == nil {
		return attestationRecord{}, false, errors.New("this gateway has no registry")
	}
	res, err := db.Query(client.WithInternalAuth(ctx),
		`SELECT platform, app_id, app_version, development, verified_boot_key, grapheneos,
		        ios_key_id, ios_public_key, ios_counter, signing_cert_digests, os_patch_level, attested_at
		   FROM device_attestations WHERE namespace_id = ? AND device_id = ? LIMIT 1`, nsID, deviceID)
	if err != nil {
		return attestationRecord{}, false, fmt.Errorf("read the device's attestation: %w", err)
	}
	if res == nil || len(res.Rows) == 0 || len(res.Rows[0]) < 12 {
		return attestationRecord{}, false, nil
	}
	row := res.Rows[0]
	var digests []string
	if err := json.Unmarshal([]byte(getStringVal(row[9])), &digests); err != nil {
		return attestationRecord{}, false, fmt.Errorf("the device's attested signing-certificate digests are not a JSON list: %w", err)
	}
	at, _, readable := parseTimestamp(row[11])
	if !readable {
		return attestationRecord{}, false, fmt.Errorf("the device's attestation time %v cannot be read", row[11])
	}
	return attestationRecord{
		platform:       attest.Platform(getStringVal(row[0])),
		appID:          getStringVal(row[1]),
		appVersion:     getStringVal(row[2]),
		development:    toInt64(row[3]) != 0,
		bootKey:        getStringVal(row[4]),
		grapheneOS:     toInt64(row[5]) != 0,
		iosKeyID:       getStringVal(row[6]),
		iosPublic:      getStringVal(row[7]),
		iosCounter:     uint32(toInt64(row[8])),
		signingDigests: digests,
		osPatchLevel:   int(toInt64(row[10])),
		attestedAt:     at,
	}, true, nil
}

// attestationForToken is the attestation an access token minted now for a
// device carries, or nil when it carries none.
//
// A session is never refused for attestation's sake: a token that cannot show
// one is minted without `att`, and the namespace's policy is what refuses it,
// on the requests it judges — so monitor mode refuses nothing, here as
// anywhere. Only a namespace whose mode is not off is read at all.
//
// An Android device's record is what it proved at its last attestation, and
// the token carries all of it that the policy can narrow afterwards — the
// package, the build, the signing-certificate digests, GrapheneOS and the OS
// patch level — so JudgeAttestation holds every request to the policy as it is
// then, as it holds the re-attestation interval. A record from before the
// digests were kept carries none and is judged "attest again". An iOS device
// proves itself again on every refresh with an App Attest assertion over the
// refresh's device proof (proof.AppAttest, over AttestationBinding(proof.ID,
// device)), its counter moved by compare-and-swap, so a replayed assertion
// loses — once per session refresh, never per request.
func (s *Service) attestationForToken(ctx context.Context, namespace, deviceID string, proof *DeviceProof) (*AttestationClaim, error) {
	if deviceID == "" {
		return nil, nil
	}
	policy, err := s.AttestationPolicyFor(sessionNamespace(namespace))
	if err != nil {
		return nil, err
	}
	if policy.Mode == AttestationOff {
		return nil, nil
	}
	_, nsID, err := s.deviceRegistry(ctx, namespace)
	if err != nil {
		return nil, err
	}
	record, ok, err := s.readAttestation(ctx, nsID, deviceID)
	if err != nil || !ok {
		return nil, err
	}
	if record.platform != attest.PlatformIOS {
		return record.claim(), nil
	}
	if proof == nil || proof.AppAttest == "" || s.attestVerifier == nil {
		return nil, nil
	}
	return s.assertIOS(ctx, policy, nsID, deviceID, record, proof)
}

// assertIOS checks an iOS refresh's assertion and moves the key's counter.
func (s *Service) assertIOS(ctx context.Context, policy AttestationPolicy, nsID interface{}, deviceID string, record attestationRecord, proof *DeviceProof) (*AttestationClaim, error) {
	assertion, err := base64.StdEncoding.DecodeString(proof.AppAttest)
	if err != nil {
		s.logAttestationRefusal(deviceID, refusal.Invalid(refusal.ReasonMalformed, "the assertion is not base64: %v", err))
		return nil, nil
	}
	public, err := base64.StdEncoding.DecodeString(record.iosPublic)
	if err != nil {
		return nil, fmt.Errorf("the device's stored App Attest key cannot be read: %w", err)
	}
	got, err := s.attestVerifier.AssertIOS(policy.Verifier(), attest.IOSKey{PublicKey: public, Counter: record.iosCounter},
		assertion, AttestationBinding(proof.ID, deviceID))
	if err != nil {
		if attest.KindOf(err) == attest.KindNone {
			return nil, err
		}
		s.logAttestationRefusal(deviceID, err)
		return nil, nil
	}
	res, err := s.db.Exec(client.WithInternalAuth(ctx),
		`UPDATE device_attestations SET ios_counter = ?, app_version = ?
		  WHERE namespace_id = ? AND device_id = ? AND ios_key_id = ? AND ios_counter = ?`,
		int64(got.Counter), got.AppVersion, nsID, deviceID, record.iosKeyID, int64(record.iosCounter))
	if err != nil {
		return nil, fmt.Errorf("move the App Attest counter: %w", err)
	}
	if affected, err := res.RowsAffected(); err != nil || affected == 0 {
		s.logAttestationRefusal(deviceID, refusal.Invalid(refusal.ReasonCounter, "another assertion moved the counter first"))
		return nil, nil
	}
	record.appVersion = got.AppVersion
	return record.claim(), nil
}

// logAttestationRefusal records why a refresh's token carries no attestation.
func (s *Service) logAttestationRefusal(deviceID string, err error) {
	if s.logger != nil {
		s.logger.ComponentWarn(logging.ComponentGeneral, "a refresh's App Attest assertion was refused; its token carries no attestation",
			zap.String("device", deviceID), zap.String("reason", string(attest.ReasonOf(err))), zap.Error(err))
	}
}

// ReissueAttested mints an access token for the session claims belong to,
// carrying the attestation the device just made, so the app does not have to
// refresh before the namespace admits it. claims are the verified token the
// attestation was posted with; everything but `att` is carried over.
func (s *Service) ReissueAttested(claims *JWTClaims, att *AttestationClaim) (string, int64, error) {
	if claims == nil || claims.Did == "" {
		return "", 0, ErrAttestationNeedsDevice
	}
	return s.GenerateBoundJWT(claims.Namespace, claims.Sub, AccessTokenLifetime, claims.Custom,
		SessionBinding{DeviceID: claims.Did, SessionID: claims.Sid, Attestation: att})
}
