package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/attest"
	"github.com/Orama-Network/orama/core/pkg/client"
	"github.com/Orama-Network/orama/core/pkg/logging"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"
)

// Service handles authentication business logic
type Service struct {
	logger           *logging.ColoredLogger
	orm              client.NetworkClient
	db               rqlite.Client // lower-level client; used where rows-affected is needed (e.g. refresh-token CAS rotation, feature #68)
	signingKey       *rsa.PrivateKey
	keyID            string
	edSigningKey     ed25519.PrivateKey
	edKeyID          string
	edKeyNamespace   string
	keyStore         *KeyStore // where a rotation writes the replacement; nil until SetKeyStore
	signingKeys      *SigningKeys
	preferEdDSA      bool
	defaultNS        string
	apiKeyHMACSecret string         // HMAC secret for hashing API keys before storage
	claimsResolver   ClaimsResolver // namespace claims-provider hook (bugboard #548); nil = none
	// apiKeyORM is the core/index RQLite client used for api_keys writes
	// on a namespace gateway (bugboard #162). Nil means use s.orm (the
	// main/index gateway, where orm already is the registry).
	apiKeyORM client.NetworkClient

	// revocations is the list of tokens this gateway refuses. Built in
	// NewService whenever there is a database to read it from.
	revocations *RevocationList

	// audit records the auth events worth keeping. Built alongside the
	// revocations, for the same reason: a Service with a database has one.
	audit *AuditLog

	// devicePolicies remembers each namespace's session policy for
	// devicePolicyStaleness, because every account-level refresh asks.
	devicePolicies policyCache[DevicePolicy]
	// signInPolicies is the same for the sign-in policy, which shares the
	// row and the staleness bound.
	signInPolicies policyCache[SignInPolicy]
	// endUserTopics is the same for the topics a namespace's end users may
	// read (bugboard #733), asked on every pub/sub read by one.
	endUserTopics policyCache[[]string]
	// perIPLimits is the same for the namespace's per-network ceilings
	// (bugboard #749), asked on every authenticated invoke.
	perIPLimits policyCache[PerIPLimits]
	// newWallets counts the wallets new to a namespace each client network
	// signs in (bugboard #749); nil until SetNewWalletCounter.
	newWallets WindowCounter

	// attestationPolicies is this gateway's copy of every namespace's
	// attestation policy, and attestVerifier what checks a device's proof
	// (feat-941). Built alongside the revocations.
	attestationPolicies *AttestationPolicies
	attestVerifier      attest.Verifier
}

// minRSAKeyBits is the smallest RSA signing key this gateway will use. 2048 is
// the floor everyone agrees on; below it the signature on an access token is
// not worth checking.
const minRSAKeyBits = 2048

// ErrNoAPIKeyHMACSecret refuses to build a service with no API key HMAC
// secret (gateway.api_key_hmac_secret).
var ErrNoAPIKeyHMACSecret = errors.New("the API key HMAC secret is required (api_key_hmac_secret, from secrets/api-key-hmac-secret)")

// ErrTokenRevoked is returned for a token whose signature verifies but which
// has been revoked — a key that was revoked, or a session that was ended.
var ErrTokenRevoked = errors.New("token revoked")

// ErrTokenExpired is returned for a token whose signature verifies but whose
// lifetime has run out. Its text is what callers have always seen.
var ErrTokenExpired = errors.New("token expired")

// NewService builds the auth service. apiKeyHMACSecret is required: every API
// key is stored and looked up as HMAC-SHA256(key, secret), and a service
// without one has no way to store or compare a key that is not the key itself.
func NewService(logger *logging.ColoredLogger, orm client.NetworkClient, signingKeyPEM string, defaultNS string, apiKeyHMACSecret string) (*Service, error) {
	if strings.TrimSpace(apiKeyHMACSecret) == "" {
		return nil, ErrNoAPIKeyHMACSecret
	}
	s := &Service{
		logger:           logger,
		orm:              orm,
		defaultNS:        defaultNS,
		apiKeyHMACSecret: apiKeyHMACSecret,
	}

	// Every Service with a database consults the revocations. There is no way
	// to build one that verifies tokens against a database and does not.
	if orm != nil {
		s.revocations = NewRevocationList(s.registryDatabase, logger)
		s.audit = NewAuditLog(s.registryDatabase, logger)
		s.attestationPolicies = NewAttestationPolicies(s.registryDatabase, logger)
	}
	// Always present, database or not: a gateway with no database still has to
	// verify what it minted itself.
	// The registry is resolved per call, so a namespace gateway that learns
	// where its core registry is after construction publishes its key there
	// rather than into the tenant's own database.
	s.signingKeys = NewSigningKeys(s.registryDatabase, logger)

	if signingKeyPEM != "" {
		block, _ := pem.Decode([]byte(signingKeyPEM))
		if block == nil {
			return nil, fmt.Errorf("failed to parse signing key PEM")
		}
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse RSA private key: %w", err)
		}
		// A short RSA key signs tokens anybody can forge. The size was never
		// checked, so whatever PEM the operator supplied was used.
		if bits := key.N.BitLen(); bits < minRSAKeyBits {
			return nil, fmt.Errorf("the configured RSA signing key is %d bits; %d is the minimum, "+
				"below which the signature on an access token is not worth checking", bits, minRSAKeyBits)
		}
		s.signingKey = key

		// Generate a simple KID from the public key hash
		pubBytes := x509.MarshalPKCS1PublicKey(&key.PublicKey)
		sum := sha256.Sum256(pubBytes)
		s.keyID = hex.EncodeToString(sum[:8])
	}

	return s, nil
}

// SetAPIKeyRegistry points API-key reads/writes at the core/index RQLite
// client. Required on namespace gateways after rqlite_dsn wins for
// deps.Client (bugboard #162): keys must not be stored in the tenant DB.
func (s *Service) SetAPIKeyRegistry(orm client.NetworkClient) {
	s.apiKeyORM = orm
}

func (s *Service) keyORM() client.NetworkClient {
	if s.apiKeyORM != nil {
		return s.apiKeyORM
	}
	return s.orm
}

// registryDatabase is the cluster registry's database, or nil when this gateway
// has none.
//
// Platform state — who may authenticate, and which keys may sign — belongs
// here and not in a tenant's own database. It is a method rather than a
// captured handle because a namespace gateway is told where its registry is
// after the auth service is built.
func (s *Service) registryDatabase() client.DatabaseClient {
	orm := s.keyORM()
	if orm == nil {
		return nil
	}
	return orm.Database()
}

// SetRqliteClient injects the lower-level rqlite client. Required for code
// paths that need rows-affected feedback for compare-and-swap operations
// (e.g. atomic refresh-token rotation, feature #68). The higher-level
// `client.NetworkClient` interface in `s.orm` does not expose RowsAffected
// on writes.
//
// Safe to call zero or one times; idempotent. Without it, methods that
// depend on CAS semantics fall back to the previous less-atomic behaviour
// (currently: RefreshToken returns ErrRotationNotConfigured).
func (s *Service) SetRqliteClient(db rqlite.Client) {
	s.db = db
}

// ClaimsResolver resolves additive, namespace-defined JWT custom claims for an
// authenticated wallet at token-mint time (bugboard #548/#920). The concrete
// implementation invokes the namespace's reserved `auth-claims-provider`
// serverless function; it MUST be fail-open (return nil, never error) so a
// missing/slow/broken provider never breaks authentication. Injected via
// SetClaimsResolver; nil = no custom claims (every namespace's default).
type ClaimsResolver interface {
	ResolveClaims(ctx context.Context, wallet, namespace string) map[string]string
}

// SetClaimsResolver wires the namespace claims-provider hook used at mint time.
func (s *Service) SetClaimsResolver(r ClaimsResolver) { s.claimsResolver = r }

// resolveCustomClaims returns the namespace's additive claims for this wallet,
// or nil. Fail-open by contract — the resolver never errors.
func (s *Service) resolveCustomClaims(ctx context.Context, wallet, namespace string) map[string]string {
	if s.claimsResolver == nil {
		return nil
	}
	return s.claimsResolver.ResolveClaims(ctx, wallet, namespace)
}

// lastKnownCustomClaims returns the most recently stored non-empty custom_claims
// for (nsID, subject) from refresh_tokens, or nil. It is the durable anti-
// fragmentation fallback (bugboard #143): once a wallet has EVER resolved
// account_id, a later TRANSIENT provider failure at login must not silently drop
// it — a device that registers under the wallet Sub instead of the stable
// account_id never receives fan-out push. Fail-soft: any error/empty → nil, so a
// missing history simply mints without custom claims (the pre-existing behaviour).
func (s *Service) lastKnownCustomClaims(ctx context.Context, nsID interface{}, subject string) map[string]string {
	internalCtx := client.WithInternalAuth(ctx)
	db := s.registryDatabase()
	if db == nil {
		return nil
	}
	// bugboard #154: exclude revoked rows. Without this a logged-out / rotated
	// row (or a stale row for a deleted-then-recreated identity) whose
	// expires_at is still in the future could resurrect stale claims (e.g. a
	// prior account_id). Only a live, non-revoked row is a trustworthy
	// last-known-good source.
	res, err := db.Query(internalCtx,
		`SELECT custom_claims FROM refresh_tokens
		 WHERE namespace_id = ? AND subject = ?
		   AND revoked_at IS NULL
		   AND custom_claims IS NOT NULL AND custom_claims != ''
		 ORDER BY expires_at DESC LIMIT 1`,
		nsID, subject)
	if err != nil || res == nil || res.Count == 0 || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return nil
	}
	cc, ok := res.Rows[0][0].(string)
	if !ok {
		return nil
	}
	return unmarshalClaims(cc)
}

// reuseLastKnownClaims implements the durable anti-fragmentation fallback
// (bugboard #143). When this namespace HAS a claims provider (claimsResolver !=
// nil) but it just failed open (transient cold-WASM stall) yielding no
// account_id, the wallet's login-time devices would register under the wallet
// Sub instead of the stable account_id and never receive fan-out push. In that
// case reuse the last account_id this wallet ever resolved. Only runs when the
// resolver is configured AND returned empty — single-credential apps (no
// provider) are unaffected, and the refresh path is untouched (it already
// replays stored claims forward). Returns the claims to mint with.
func (s *Service) reuseLastKnownClaims(ctx context.Context, nsID interface{}, wallet, namespace string, custom map[string]string) map[string]string {
	if s.claimsResolver == nil || len(custom) > 0 {
		return custom
	}
	lkg := s.lastKnownCustomClaims(ctx, nsID, wallet)
	if len(lkg) == 0 {
		return custom
	}
	s.logger.ComponentInfo(logging.ComponentGeneral,
		"claims provider returned empty at login; reusing last-known-good custom claims (anti-fragmentation)",
		zap.String("namespace", namespace), zap.String("subject", wallet))
	return lkg
}

// ErrRotationNotConfigured is returned by RefreshToken when the service
// wasn't given an rqlite client — refusing to rotate without atomicity
// guarantees is safer than rotating non-atomically.
var ErrRotationNotConfigured = fmt.Errorf("auth service not configured for atomic refresh-token rotation (missing rqlite client)")

// NormalizeWallet canonicalises a wallet address for storage and lookup.
//
// An EVM address reaches the gateway in different cases: EIP-55 checksummed
// from one client, lowercase from another, and both are the same account. Every
// row keyed by a wallet is matched by exact string equality, so unless both the
// write and the read normalise the same way one login records ownership that a
// later login cannot find. An EVM address (0x…) is therefore lowercased.
//
// Nothing else is. A Solana address is base58, where case is part of the value:
// lowercasing one names a different key, or none. Doing it made every Solana
// session's subject an address its holder does not own, so an application that
// compares the subject with the user's address — AnChat's signup does, strictly
// for Solana — refused every Solana user.
func NormalizeWallet(wallet string) string {
	wallet = strings.TrimSpace(wallet)
	if isEVMAddress(wallet) {
		return strings.ToLower(wallet)
	}
	return wallet
}

// isEVMAddress reports whether a wallet is written as an EVM address, whose
// case carries only the EIP-55 checksum.
func isEVMAddress(wallet string) bool {
	return len(wallet) >= 2 && wallet[0] == '0' && (wallet[1] == 'x' || wallet[1] == 'X')
}

// HashAPIKey returns the HMAC-SHA256 hash of an API key: the only form a key is
// stored, revoked or looked up by. It used to return the raw key when no secret
// was configured, so a gateway missing the secret stored the keys it minted in
// plaintext and compared presented keys against plaintext rows.
func (s *Service) HashAPIKey(key string) string {
	return HmacSHA256Hex(key, s.apiKeyHMACSecret)
}

// SetEdDSAKey configures the Ed25519 key this gateway signs with.
//
// namespace is what the key is bound to: a namespace gateway signs only for its
// own tenant, and a token signed with its key is refused if it claims another.
// An empty namespace is the index gateway's key, which is the control plane and
// is bound to nothing.
func (s *Service) SetEdDSAKey(privKey ed25519.PrivateKey, namespace string) {
	if s.signingKeys == nil {
		// A Service built as a struct literal rather than by NewService — the
		// shape several tests use — has no key set yet, and a gateway that
		// signs a token has to be able to verify it.
		s.signingKeys = NewSigningKeys(s.registryDatabase, s.logger)
	}
	s.edSigningKey = privKey
	pub := privKey.Public().(ed25519.PublicKey)
	s.edKeyID = KeyIDFor(pub)
	s.edKeyNamespace = namespace
	s.preferEdDSA = true
	s.signingKeys.Add(SigningKey{KID: s.edKeyID, Namespace: namespace, Public: pub})
}

// SetKeyStore sets where Rotate stores the replacement key.
func (s *Service) SetKeyStore(store KeyStore) { s.keyStore = &store }

// SigningKeys is every key this gateway will accept a token from.
func (s *Service) SigningKeys() *SigningKeys { return s.signingKeys }

// SigningKID is the key this gateway signs with.
func (s *Service) SigningKID() string { return s.edKeyID }

// PublishSigningKey records this gateway's key so the rest of the cluster will
// accept what it mints.
func (s *Service) PublishSigningKey(ctx context.Context) error {
	if s.edSigningKey == nil {
		return nil
	}
	return s.signingKeys.Publish(ctx, SigningKey{
		KID:       s.edKeyID,
		Namespace: s.edKeyNamespace,
		Public:    s.edSigningKey.Public().(ed25519.PublicKey),
	})
}

// insertNonce records a challenge this gateway has just issued, so that
// ConsumeNonce can claim it exactly once later.
//
// The namespace must already exist. This used to be an INSERT OR IGNORE, so an
// unauthenticated POST to /v1/auth/challenge created a namespace for any name
// at all — squatting a name was free, and signing in to a name nobody had taken
// silently created it. Creating one is its own authenticated call now.
func (s *Service) insertNonce(ctx context.Context, wallet, nonce, purpose, namespace string) error {
	internalCtx := client.WithInternalAuth(ctx)
	db := s.registryDatabase()
	if db == nil {
		return fmt.Errorf("client not initialized")
	}

	nsID, err := s.lookupNamespaceID(ctx, namespace)
	if err != nil {
		return fmt.Errorf("failed to resolve namespace %q: %w", namespace, err)
	}
	if nsID == nil {
		return &ErrNamespaceUnknown{Namespace: namespace}
	}

	// A challenge writes a Raft-replicated row, and nothing proves the caller
	// owns the wallet it names. Without a ceiling on how many can be
	// outstanding at once, a grind fills the table for that wallet.
	walletKey := normalizeNonceWallet(wallet)
	if err := s.checkOutstandingNonces(internalCtx, db, nsID, walletKey, namespace); err != nil {
		return err
	}

	// ConsumeNonce matches this row by exact string equality, so both sides
	// normalise the wallet the same way. The expiry here and the Expiration
	// Time in the signed message are the same ChallengeTTL, checked
	// independently: the message is what the wallet showed the user, this row
	// is what the gateway will honour.
	if _, err := db.Query(internalCtx,
		"INSERT INTO nonces(namespace_id, wallet, nonce, purpose, expires_at) VALUES (?, ?, ?, ?, datetime('now', ?))",
		nsID, walletKey, nonce, purpose, fmt.Sprintf("+%d seconds", int64(ChallengeTTL.Seconds())),
	); err != nil {
		return fmt.Errorf("failed to store nonce: %w", err)
	}
	return nil
}

// verifyEthSignature checks an EIP-191 personal_sign signature over message.
func (s *Service) verifyEthSignature(wallet, message, signature string) (bool, error) {
	msg := []byte(message)
	prefix := []byte("\x19Ethereum Signed Message:\n" + strconv.Itoa(len(msg)))
	hash := ethcrypto.Keccak256(prefix, msg)

	sigHex := strings.TrimSpace(signature)
	if strings.HasPrefix(sigHex, "0x") || strings.HasPrefix(sigHex, "0X") {
		sigHex = sigHex[2:]
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != 65 {
		return false, fmt.Errorf("invalid signature format")
	}

	if sig[64] >= 27 {
		sig[64] -= 27
	}

	pub, err := ethcrypto.SigToPub(hash, sig)
	if err != nil {
		return false, fmt.Errorf("signature recovery failed: %w", err)
	}

	addr := ethcrypto.PubkeyToAddress(*pub).Hex()
	want := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(wallet, "0x"), "0X"))
	got := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(addr, "0x"), "0X"))

	return got == want, nil
}

// verifySolSignature checks a raw ed25519 signature over message. Solana signs
// the message bytes with no prefix, which is what SIWS specifies.
func (s *Service) verifySolSignature(wallet, message, signature string) (bool, error) {
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false, fmt.Errorf("invalid base64 signature: %w", err)
	}
	if len(sig) != 64 {
		return false, fmt.Errorf("invalid signature length: expected 64 bytes, got %d", len(sig))
	}

	pubKeyBytes, err := s.Base58Decode(wallet)
	if err != nil {
		return false, fmt.Errorf("invalid wallet address: %w", err)
	}
	if len(pubKeyBytes) != 32 {
		return false, fmt.Errorf("invalid public key length: expected 32 bytes, got %d", len(pubKeyBytes))
	}

	return ed25519.Verify(ed25519.PublicKey(pubKeyBytes), []byte(message), sig), nil
}

// IssueTokens generates access and refresh tokens for a verified wallet, bound
// to the account alone.
func (s *Service) IssueTokens(ctx context.Context, wallet, namespace string) (string, string, int64, error) {
	return s.IssueDeviceTokens(ctx, wallet, namespace, "")
}

// IssueDeviceTokens issues a session bound to a device, or to the account alone
// when deviceID is empty. The caller has established that the device is the
// account's and active; a session bound to it is refreshed only with the
// device's proof (see RefreshToken).
//
// Every session gets an id of its own, carried in its access tokens as `sid`
// and kept across refresh-token rotations, so ending the session refuses every
// access token it minted.
func (s *Service) IssueDeviceTokens(ctx context.Context, wallet, namespace, deviceID string) (string, string, int64, error) {
	return s.issueSession(ctx, wallet, namespace, deviceID, false)
}

// IssuePendingDeviceTokens issues the restricted session of a device still
// pending under the function policy. The caller has enrolled the device as the
// account's, pending. Its access tokens are for AudiencePendingDevice; its
// refresh mints the same until a function approves the device, and a full
// session from then on (checkSessionRefresh).
func (s *Service) IssuePendingDeviceTokens(ctx context.Context, wallet, namespace, deviceID string) (string, string, int64, error) {
	if deviceID == "" {
		return "", "", 0, fmt.Errorf("%w: a pending session is a device's", ErrDeviceKeyInvalid)
	}
	return s.issueSession(ctx, wallet, namespace, deviceID, true)
}

func (s *Service) issueSession(ctx context.Context, wallet, namespace, deviceID string, pending bool) (string, string, int64, error) {
	if s.signingKey == nil {
		return "", "", 0, fmt.Errorf("signing key unavailable")
	}
	// Every path that hands out a session passes here, so this is where the
	// namespace's sign-in rule is held. A device-link claim reached it with
	// no check: a wallet the namespace no longer admits — sign-in closed
	// again, or its grant revoked — kept minting sessions by approving its
	// own next link from a device it still held.
	if err := s.RequireSignInAllowed(ctx, wallet, namespace); err != nil {
		return "", "", 0, err
	}

	// Resolve namespace-defined additive claims (bugboard #548) ONCE at mint
	// time. Stored with the refresh token below and replayed across rotations
	// so the 15-min refresh path never re-invokes the provider.
	custom := s.resolveCustomClaims(ctx, wallet, namespace)

	nsID, err := s.ResolveNamespaceID(ctx, namespace)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to resolve namespace ID: %w", err)
	}

	custom = s.reuseLastKnownClaims(ctx, nsID, wallet, namespace, custom)

	sessionID, err := newTokenID()
	if err != nil {
		return "", "", 0, err
	}
	att, err := s.attestationForToken(ctx, namespace, deviceID, nil)
	if err != nil {
		return "", "", 0, fmt.Errorf("read the device's attestation: %w", err)
	}
	binding := SessionBinding{DeviceID: deviceID, SessionID: sessionID, Attestation: att, Pending: pending}
	token, expUnix, err := s.GenerateBoundJWT(namespace, wallet, AccessTokenLifetime, custom, binding)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to generate JWT: %w", err)
	}

	// Create refresh token (30d)
	refresh, err := mintRefreshToken(deviceID != "")
	if err != nil {
		return "", "", 0, err
	}

	internalCtx := client.WithInternalAuth(ctx)
	db := s.registryDatabase()
	if db == nil {
		return "", "", 0, fmt.Errorf("client not initialized")
	}
	hashedRefresh := refreshTokenHash(refresh)
	if _, err := db.Query(internalCtx, insertRefreshTokenSQL,
		nsID, wallet, hashedRefresh, "gateway", marshalClaims(custom), nullable(deviceID), sessionID,
	); err != nil {
		return "", "", 0, fmt.Errorf("failed to store refresh token: %w", err)
	}

	return token, refresh, expUnix, nil
}

// ErrRefreshTokenReplay is returned when a refresh token's CAS lock is lost —
// the row was already revoked between our read and our write, meaning either
// another concurrent request rotated it OR an attacker is replaying a stolen
// token after the legitimate client refreshed. Callers should treat this as
// a potential security event and surface 401 to the client; the service
// itself emits a WARN log so operators can audit.
//
// This is the tripwire promised by RFC 9700 §4.12 (refresh-token rotation).
var ErrRefreshTokenReplay = fmt.Errorf("refresh token already rotated or invalid")

// ErrRefreshTransient is returned when refresh-token rotation fails for a
// RETRYABLE reason — an rqlite-layer error rather than a genuine bad/expired
// token. Bugboard #125: during a rolling gateway restart the rqlite leader is
// briefly unavailable (re-election window), so the lookup/rotation errors;
// collapsing that into "invalid token" forces a 401 → full SIWE re-auth, which
// is impossible on a locked device answering a VoIP-woken call. Callers MUST
// surface this as a retryable 503, NOT a 401, so the client retries within the
// ring window instead of tearing down the session.
var ErrRefreshTransient = fmt.Errorf("refresh token rotation temporarily unavailable")

const (
	// refreshSelectRetries bounds how many times the refresh lookup is retried
	// when the rqlite read errors (transient leader unavailability). The read
	// is idempotent and happens BEFORE any write, so retrying is safe.
	refreshSelectRetries = 3
	// refreshSelectRetryDelay is the backoff between lookup retries. Three
	// tries × 250ms rides out a brief leader re-election without adding
	// meaningful latency to the common (healthy-leader) path.
	refreshSelectRetryDelay = 250 * time.Millisecond
	// refreshReuseGrace is how long after a refresh token is rotated (revoked)
	// the gateway will still accept it ONE more time, to recover a client whose
	// rotation response was lost in transit — otherwise the retry dead-ends in a
	// 401 → SIWE, impossible on a VoIP-woken locked screen (bugboard #125, RFC
	// 9700 §4.13.2). Kept short, and single-use via grace_used_at, so a stolen
	// token cannot be replayed at leisure.
	refreshReuseGrace = 60 * time.Second
)

// RefreshToken validates the supplied refresh token, atomically rotates it
// (revokes the old, mints a new), and returns a fresh access token alongside
// the rotated refresh token.
//
// Rotation is the RFC 9700 BCP §4.12 / feature #68 behaviour:
//
//  1. SELECT the subject for the supplied token (must be unrevoked + unexpired)
//  2. UPDATE revoked_at = now() WHERE token = ? AND revoked_at IS NULL
//     -- this is the atomic CAS. If RowsAffected == 0, the race was lost
//     -- (concurrent rotation or token-replay attack); we fail closed and
//     -- emit a security log line so operators can investigate.
//  3. Generate a fresh refresh-token + fresh access JWT
//  4. INSERT the new refresh-token row
//  5. Return both
//
// Failure modes:
//   - Token invalid/expired at step 1 → standard "invalid or expired" error,
//     no security event.
//   - CAS lost at step 2 → ErrRefreshTokenReplay, WARN logged with subject +
//     namespace. The client sees 401.
//   - Crash between step 2 and step 4 → user is left with revoked old + no
//     new, forcing re-login. Acceptable: degrades to re-auth, never enables
//     double-use of a single refresh token.
//
// Returns:
//
//	accessToken     — newly minted short-lived JWT (15 min)
//	newRefreshToken — newly minted long-lived refresh token (30 days)
//	subject         — wallet/subject claim of the refreshed session
//	expUnix         — access token expiry (unix seconds)
//	err             — non-nil on any failure; ErrRefreshTokenReplay for CAS loss
func (s *Service) RefreshToken(ctx context.Context, refreshToken, namespace string, proof *DeviceProof) (accessToken, newRefreshToken, subject string, expUnix int64, err error) {
	// Atomic rotation requires the lower-level rqlite client (RowsAffected
	// feedback isn't exposed by the higher-level client.NetworkClient).
	// Refuse to rotate non-atomically — see ErrRotationNotConfigured.
	if s.db == nil {
		return "", "", "", 0, ErrRotationNotConfigured
	}

	internalCtx := client.WithInternalAuth(ctx)
	ormDB := s.registryDatabase()
	if ormDB == nil {
		// The gateway's own fault, not the token's: surfacing it as a
		// rejection (401) would end a CLI session that is still valid.
		return "", "", "", 0, fmt.Errorf("%w: registry client not initialized", ErrRefreshTransient)
	}

	// The same name the lookup below resolves: an empty namespace is the
	// lobby. Passed on as given, it minted a token naming no namespace,
	// which the permission check did not recognise as the lobby.
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = LobbyNamespace
	}

	nsID, err := s.ResolveNamespaceID(ctx, namespace)
	var unknown *ErrNoSuchNamespace
	if errors.As(err, &unknown) {
		// The registry answered: no such namespace (deleted, or never
		// created). No token of it can be renewed, so this is the refusal
		// (→ 401), not a retry the client would repeat for ever.
		return "", "", "", 0, err
	}
	if err != nil {
		// Bugboard #125: namespace resolution runs an rqlite query BEFORE the
		// token lookup, so a leader re-election during a rolling restart fails
		// here too. Treat it as retryable (→ 503), not a bad token (→ 401).
		s.logger.ComponentWarn(logging.ComponentGeneral,
			"refresh namespace resolution failed (transient, surfacing retryable)",
			zap.String("namespace", namespace),
			zap.Error(err))
		return "", "", "", 0, ErrRefreshTransient
	}

	hashedRefresh := refreshTokenHash(refreshToken)

	// Step 1: read the subject. Tells us who the token belongs to AND
	// validates that it's currently usable (not revoked, not expired).
	//
	// Bugboard #125: distinguish a TRANSIENT rqlite error (leader briefly
	// unavailable during a rolling restart) from a GENUINE token miss. The
	// read is idempotent and pre-write, so we retry it a few times; only after
	// exhausting retries do we surface ErrRefreshTransient (→ 503, client
	// retries). An actual empty result (Count == 0) is a real bad/expired
	// token → "invalid or expired" (→ 401). Collapsing the two used to 401 a
	// valid session during every restart, defeating the VoIP-wake refresh.
	selectQ := `SELECT subject, custom_claims, COALESCE(device_id, ''), COALESCE(session_id, '') FROM refresh_tokens
	            WHERE namespace_id = ? AND token = ?
	              AND revoked_at IS NULL
	              AND (expires_at IS NULL OR expires_at > datetime('now'))
	            LIMIT 1`
	var res *client.QueryResult
	var selErr error
	for attempt := 0; attempt < refreshSelectRetries; attempt++ {
		res, selErr = ormDB.Query(internalCtx, selectQ, nsID, hashedRefresh)
		if selErr == nil && res != nil {
			break
		}
		if attempt < refreshSelectRetries-1 {
			time.Sleep(refreshSelectRetryDelay)
		}
	}
	if selErr != nil || res == nil {
		// rqlite error persisted across retries — leader likely mid-election.
		// Retryable, NOT an invalid token.
		s.logger.ComponentWarn(logging.ComponentGeneral,
			"refresh token lookup failed (transient rqlite error, surfacing retryable)",
			zap.String("namespace", namespace),
			zap.Error(selErr))
		return "", "", "", 0, ErrRefreshTransient
	}
	// graceRecovery is set when the presented token was NOT in the active set
	// but qualifies for the bugboard #125 single-use reuse grace (a just-
	// rotated token whose rotation response was lost). In that case the old row
	// is already revoked, so we SKIP the revoke CAS (step 2) — the grace CAS
	// inside tryRefreshReuseGrace is our single-use lock — and go straight to
	// minting a fresh session.
	graceRecovery := false
	var session refreshRow
	if res.Count == 0 {
		gSession, gOK, gErr := s.lookupReuseGrace(internalCtx, ormDB, nsID, hashedRefresh)
		if gErr != nil {
			// Transient rqlite error during the grace lookup/claim — retryable,
			// not a verdict on the token (bugboard #125).
			s.logger.ComponentWarn(logging.ComponentGeneral,
				"refresh reuse-grace lookup failed (transient rqlite error, surfacing retryable)",
				zap.String("namespace", namespace), zap.Error(gErr))
			return "", "", "", 0, ErrRefreshTransient
		}
		if !gOK {
			// Not found, expired, revoked outside the grace, or its grace
			// already consumed — a real bad token. One that was issued and
			// has since been spent is a replay, and is told apart so the
			// handler records it: only a concurrent race used to be.
			spent, deviceID, serr := s.refreshTokenSpent(internalCtx, ormDB, nsID, hashedRefresh)
			if serr != nil {
				s.logger.ComponentWarn(logging.ComponentGeneral,
					"refresh replay check failed (transient rqlite error, surfacing retryable)",
					zap.String("namespace", namespace), zap.Error(serr))
				return "", "", "", 0, ErrRefreshTransient
			}
			// Revoking a device revokes its refresh tokens. A revoked token of
			// a revoked device is refused for the device, with its
			// revocation (DEVICE_REVOKED), not as a replay.
			if spent && deviceID != "" {
				if err := s.refusedForDevice(ctx, namespace, deviceID); err != nil {
					return "", "", "", 0, err
				}
			}
			if spent {
				s.logger.ComponentWarn(logging.ComponentGeneral,
					"refresh token replay: a revoked token was presented outside its reuse grace",
					zap.String("namespace", namespace))
				return "", "", "", 0, ErrRefreshTokenReplay
			}
			return "", "", "", 0, fmt.Errorf("invalid or expired refresh token")
		}
		session = gSession
		graceRecovery = true
		s.logger.ComponentInfo(logging.ComponentGeneral,
			"refresh token reuse-grace recovery (lost-response retry, single-use)",
			zap.String("namespace", namespace), zap.String("subject", RedactSubject(session.subject)))
	} else if len(res.Rows) > 0 {
		// custom_claims (bugboard #548) — resolved once at login, replayed on
		// every rotation so the refresh path never re-invokes the provider.
		session = readRefreshRow(res.Rows[0])
	}
	subject = session.subject
	custom := session.custom

	// A session bound to a device is refreshed by the device, not by whoever
	// holds its refresh token. Checked before the rotation below, so a thief
	// holding only the token cannot burn the device's session by trying.
	pending, err := s.checkSessionRefresh(ctx, namespace, session, refreshToken, proof)
	if err != nil {
		return "", "", "", 0, err
	}
	// Only now is the reuse grace spent: a caller who could not show the device
	// must not use up the lost-response recovery of the client that can.
	if graceRecovery {
		claimed, err := s.claimReuseGrace(internalCtx, nsID, hashedRefresh)
		if err != nil {
			s.logger.ComponentWarn(logging.ComponentGeneral,
				"refresh reuse-grace claim failed (transient rqlite error, surfacing retryable)",
				zap.String("namespace", namespace), zap.Error(err))
			return "", "", "", 0, ErrRefreshTransient
		}
		if !claimed {
			return "", "", "", 0, fmt.Errorf("invalid or expired refresh token")
		}
	}
	// A session issued before sessions carried an id gets one now, and keeps
	// it from here on.
	if session.sessionID == "" {
		if session.sessionID, err = newTokenID(); err != nil {
			return "", "", "", 0, fmt.Errorf("%w: generate session id: %v", ErrRefreshTransient, err)
		}
	}

	// bugboard #154: an empty-claims session must be able to self-heal on
	// refresh. A JWT minted BEFORE the app's account row exists (e.g. AnChat
	// signup: token minted at SIWE-verify, account_id resolves 5-25s later)
	// stored empty custom_claims; the pre-existing behaviour replayed that
	// emptiness for the whole ~30-day refresh chain, so account_id-gated
	// features (push routing) stayed broken until a manual re-login. When the
	// stored claims are EMPTY and this namespace has a provider, RE-RESOLVE
	// here so the session converges within one refresh cycle (~15 min).
	// Scoped to empty-claims sessions only: a healthy (non-empty) session never
	// touches the provider, so the hot path pays nothing. resolveCustomClaims
	// is fail-open (never errors) and we only overwrite on a non-empty result,
	// so a still-unavailable provider leaves custom empty and refresh proceeds
	// unchanged. Self-terminating: once healed, subsequent refreshes see
	// non-empty claims and skip this entirely. subject is the wallet (stored as
	// the refresh-row subject at mint), exactly what the resolver expects.
	if len(custom) == 0 && s.claimsResolver != nil {
		if reResolved := s.resolveCustomClaims(internalCtx, subject, namespace); len(reResolved) > 0 {
			custom = reResolved
		}
	}

	// What the device's attestation proved, when its namespace attests. Read
	// before the rotation below, so a registry that cannot say leaves the
	// refresh token unspent for the client's retry.
	att, err := s.attestationForToken(ctx, namespace, session.deviceID, proof)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("%w: read the device's attestation: %v", ErrRefreshTransient, err)
	}

	// Step 2: atomic CAS — revoke the old row. RowsAffected is the lock.
	// Two concurrent calls with the same refresh token: exactly one wins
	// the UPDATE (RowsAffected == 1); the other sees RowsAffected == 0
	// and bails with the replay tripwire.
	//
	// Skipped on a grace recovery (bugboard #125): the token is ALREADY
	// revoked, so this CAS would always see RowsAffected == 0 and mis-fire the
	// replay tripwire. The single-use grace CAS (grace_used_at) inside
	// claimReuseGrace already served as the lock for this path.
	if !graceRecovery {
		updRes, err := s.db.Exec(internalCtx,
			`UPDATE refresh_tokens SET revoked_at = datetime('now')
			 WHERE namespace_id = ? AND token = ? AND revoked_at IS NULL`,
			nsID, hashedRefresh)
		if err != nil {
			// rqlite write error (leader unavailable) — retryable, not a bad
			// token. No row was revoked, so a client retry is safe (bugboard #125).
			s.logger.ComponentWarn(logging.ComponentGeneral,
				"refresh token revoke failed (transient rqlite error, surfacing retryable)",
				zap.String("namespace", namespace),
				zap.Error(err))
			return "", "", "", 0, ErrRefreshTransient
		}
		affected, _ := updRes.RowsAffected()
		if affected == 0 {
			// Race lost OR replay attempt: token was unrevoked at step 1 but
			// already revoked by step 2, meaning a concurrent call rotated it
			// in between. Could be benign (same client retrying due to a
			// transient network error) or malicious (stolen token + race).
			// Either way: fail closed, log it, let the operator investigate.
			s.logger.ComponentWarn(logging.ComponentGeneral,
				"refresh token rotation: concurrent use detected (possible replay)",
				zap.String("namespace", namespace),
				zap.String("subject", RedactSubject(subject)))
			return "", "", "", 0, ErrRefreshTokenReplay
		}
	}

	// Step 3: mint the new access JWT, carrying forward the stored custom
	// claims so a rotated token keeps the same account_id etc. (bugboard #548).
	accessToken, expUnix, err = s.GenerateBoundJWT(namespace, subject, AccessTokenLifetime, custom,
		SessionBinding{DeviceID: session.deviceID, SessionID: session.sessionID, Attestation: att, Pending: pending})
	if err != nil {
		return "", "", "", 0, fmt.Errorf("%w: generate access token: %v", ErrRefreshTransient, err)
	}

	// Step 4: mint and persist a new refresh token (32-byte random,
	// base64-url-encoded; stored hashed). 30-day TTL. Note: if this
	// INSERT fails after the UPDATE succeeded (step 2), the user is left
	// with revoked old + no new and must re-authenticate. Acceptable —
	// degrades to re-auth, never to double-use of a single refresh token.
	newRefreshToken, err = mintRefreshToken(session.deviceID != "")
	if err != nil {
		return "", "", "", 0, fmt.Errorf("%w: mint refresh token: %v", ErrRefreshTransient, err)
	}
	hashedNew := refreshTokenHash(newRefreshToken)
	// Re-marshal from the parsed map (not the raw stored string) so the new
	// row and the freshly-minted access token are provably consistent and
	// self-healing — a malformed stored blob converges to "" on both sides
	// rather than being propagated forward verbatim. custom_claims is written
	// ONLY here and in IssueTokens, both from a sanitized map (bugboard #548).
	if _, err := ormDB.Query(internalCtx, insertRefreshTokenSQL,
		nsID, subject, hashedNew, "gateway", marshalClaims(custom), nullable(session.deviceID), session.sessionID); err != nil {
		// The old token is already revoked (step 2). A retryable error here
		// leaves the client to re-attempt — which will re-auth since the old
		// token is gone — but that's strictly better than masking a transient
		// failure as a permanent 401 (bugboard #125). Surface retryable.
		s.logger.ComponentWarn(logging.ComponentGeneral,
			"refresh token store failed after revoke (transient rqlite error)",
			zap.String("namespace", namespace),
			zap.Error(err))
		return "", "", "", 0, ErrRefreshTransient
	}

	return accessToken, newRefreshToken, subject, expUnix, nil
}

// lookupReuseGrace finds a rotated refresh token still inside the bounded,
// single-use reuse grace (bugboard #125, RFC 9700 §4.13.2). A token revoked
// within refreshReuseGrace whose grace_used_at is still NULL may be accepted
// ONCE more — recovering a client that lost its rotation response in transit (a
// reconnect storm during a gateway roll) before it dead-ends in a 401 → SIWE.
//
// It only reads. claimReuseGrace spends the grace, after the caller has shown
// what the session requires — a device-bound one, the device's proof.
//
// Returns (session, true, nil) for an eligible row; (—, false, nil) when there
// is none: the token was revoked outside the grace window, it has expired, or
// the grace was already consumed (caller → 401). A non-nil error is a
// transient rqlite failure (caller → 503).
func (s *Service) lookupReuseGrace(ctx context.Context, ormDB client.DatabaseClient, nsID interface{}, hashedRefresh string) (session refreshRow, ok bool, err error) {
	graceArg := fmt.Sprintf("-%d seconds", int(refreshReuseGrace.Seconds()))
	sel := `SELECT subject, custom_claims, COALESCE(device_id, ''), COALESCE(session_id, '') FROM refresh_tokens
	        WHERE namespace_id = ? AND token = ?
	          AND revoked_at IS NOT NULL
	          AND revoked_at > datetime('now', ?)
	          AND grace_used_at IS NULL
	          AND (expires_at IS NULL OR expires_at > datetime('now'))
	        LIMIT 1`
	res, qerr := ormDB.Query(ctx, sel, nsID, hashedRefresh, graceArg)
	if qerr != nil {
		return refreshRow{}, false, qerr // transient rqlite error → caller 503
	}
	if res == nil || res.Count == 0 || len(res.Rows) == 0 {
		return refreshRow{}, false, nil // no eligible grace row → caller 401
	}
	session = readRefreshRow(res.Rows[0])
	if session.subject == "" {
		return refreshRow{}, false, nil // defensive: never grace-mint an anonymous session
	}
	return session, true, nil
}

// refreshTokenSpent reports whether a refresh token was issued in this
// namespace and has since been revoked — rotated away, or ended by a logout. A
// token nobody ever issued, or one that merely expired, is not: presenting it is
// a mistake, and presenting a spent one is what a stolen token looks like.
func (s *Service) refreshTokenSpent(ctx context.Context, ormDB client.DatabaseClient, nsID interface{}, hashedRefresh string) (bool, string, error) {
	res, err := ormDB.Query(ctx,
		`SELECT COALESCE(device_id, '') FROM refresh_tokens WHERE namespace_id = ? AND token = ? AND revoked_at IS NOT NULL LIMIT 1`,
		nsID, hashedRefresh)
	if err != nil {
		return false, "", err
	}
	if res == nil || res.Count == 0 || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return false, "", nil
	}
	return true, getStringVal(res.Rows[0][0]), nil
}

// refusedForDevice is the refusal of a revoked refresh token bound to
// deviceID when that device is revoked (a DeviceRevokedError), nil when it is
// not, and ErrRefreshTransient when the device cannot be read.
func (s *Service) refusedForDevice(ctx context.Context, namespace, deviceID string) error {
	d, err := s.Device(ctx, namespace, deviceID)
	switch {
	case errors.Is(err, ErrDeviceNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("%w: read the token's device: %v", ErrRefreshTransient, err)
	case d.State == DeviceStateRevoked:
		return revokedError(d)
	}
	return nil
}

// claimReuseGrace spends a rotated token's reuse grace: a single-use CAS.
// Exactly one caller wins; a concurrent replay of the same just-revoked token
// sees RowsAffected == 0 → no grace. The time-window predicate is repeated so
// the claim cannot succeed on a row that aged out of the window since it was
// read. Security: the grace is both short-windowed AND single-use, so a stolen
// token cannot be replayed repeatedly; and it never touches the
// concurrent-rotation replay tripwire, which fires on the active path only.
func (s *Service) claimReuseGrace(ctx context.Context, nsID interface{}, hashedRefresh string) (bool, error) {
	graceArg := fmt.Sprintf("-%d seconds", int(refreshReuseGrace.Seconds()))
	updRes, err := s.db.Exec(ctx,
		`UPDATE refresh_tokens SET grace_used_at = datetime('now')
		 WHERE namespace_id = ? AND token = ? AND grace_used_at IS NULL
		   AND revoked_at IS NOT NULL AND revoked_at > datetime('now', ?)`,
		nsID, hashedRefresh, graceArg)
	if err != nil {
		return false, err
	}
	affected, _ := updRes.RowsAffected()
	return affected > 0, nil
}

// RevokeToken revokes a specific refresh token or all tokens for a subject
func (s *Service) RevokeToken(ctx context.Context, namespace, token string, all bool, subject string) error {
	internalCtx := client.WithInternalAuth(ctx)
	db := s.registryDatabase()
	if db == nil {
		return fmt.Errorf("client not initialized")
	}

	nsID, err := s.ResolveNamespaceID(ctx, namespace)
	if err != nil {
		return err
	}

	// Explicit revocation (logout / revoke-all) ALSO burns the reuse-grace slot
	// (grace_used_at) so a deliberately-revoked token can NEVER be recovered by
	// the bugboard #125 reuse grace. Rotation does not go through RevokeToken,
	// so the legitimate lost-response grace path is unaffected; this only closes
	// the logout-bypass where a just-logged-out token would otherwise be
	// grace-eligible for the 60s window.
	//
	// all goes first: a logout of every session that also names the caller's
	// own refresh token used to revoke only that token, and every other
	// session of the wallet kept refreshing.
	//
	// Every session means every namespace: the access tokens are revoked for
	// the subject everywhere (RevokeAllSessions), and a refresh token left in
	// another namespace would mint new ones (docs/whitepaper/technical-reference/vol1/13-identity.md: "ends every
	// session of the wallet at once").
	if all && subject != "" {
		_, err := db.Query(internalCtx, "UPDATE refresh_tokens SET revoked_at = datetime('now'), grace_used_at = datetime('now') WHERE subject = ? AND revoked_at IS NULL", subject)
		return err
	}

	if token != "" {
		hashedToken := refreshTokenHash(token)
		_, err := db.Query(internalCtx, "UPDATE refresh_tokens SET revoked_at = datetime('now'), grace_used_at = datetime('now') WHERE namespace_id = ? AND token = ? AND revoked_at IS NULL", nsID, hashedToken)
		return err
	}

	return ErrNothingToRevoke
}

// ErrNothingToRevoke is a logout that named no refresh token and did not ask
// for every session: the caller's mistake, not the gateway's.
var ErrNothingToRevoke = errors.New("nothing to revoke: send the refresh_token to end, or all=true with a signed-in session")

// GetOrCreateAPIKey returns an existing API key or creates a new one for a wallet in a namespace.
//
// It refuses when the namespace belongs to another wallet. This used to be the
// takeover: the ownership row it wrote unconditionally made any wallet that
// named an existing namespace an admin co-owner of it, and the key it returned
// carried no scopes, which the read path treated as admin. Both halves are
// closed here — ownership is claimed before anything is minted, and the key is
// minted with the grant written down.
func (s *Service) GetOrCreateAPIKey(ctx context.Context, wallet, namespace string) (string, error) {
	internalCtx := client.WithInternalAuth(ctx)
	db := s.keyORM().Database()

	nsID, err := s.resolveKeyNamespaceID(ctx, namespace)
	if err != nil {
		return "", err
	}

	// The lobby has no keys. A wallet there holds no role and owns nothing;
	// what it gets from signing in is a session, and the one thing that session
	// reaches is POST /v1/namespaces. Minting a key here would hand every
	// wallet on the internet a credential in the index gateway's own namespace.
	if IsLobbyNamespace(namespace) {
		return "", fmt.Errorf("%w: %q is where a wallet stands before it owns anything; "+
			"create a namespace with 'orama namespace create <name>'", ErrNoKeysInLobby, LobbyNamespace)
	}

	// Membership first, and the role it carries decides what the key holds.
	// The key used to be minted with admin whatever the caller's role was, so
	// a reader or a runtime member signing in was handed the full control
	// plane by the login itself.
	grant, err := s.GrantIn(ctx, db, nsID, PrincipalWallet, NormalizeWallet(wallet))
	if errors.Is(err, ErrNotAMember) {
		// A wallet signed in to a namespace with open sign-in holds no grant,
		// and a key is a member's credential: it has a session and nothing
		// to mint a key from.
		return "", fmt.Errorf("%w: this wallet holds no grant in %q: %w", ErrNoKeyForRole, namespace, ErrNotAMember)
	}
	if err != nil {
		return "", err
	}
	ownerScopes := grant.Scopes().Canonical()
	if ownerScopes == "" {
		return "", fmt.Errorf("%w: this wallet's role in %q is %s", ErrNoKeyForRole, namespace, grant.Role)
	}

	// The wallet's previous key, if it has one. Its id, not its value: what is
	// stored is an HMAC of the key, and this used to SELECT that column and
	// hand it back as the caller's API key.
	//
	// That worked only while no HMAC secret was configured. Production always
	// configures one, so a returning owner's second login answered with the
	// hash — a string that hashes to something else again and is refused
	// everywhere. The raw key is shown once and is not recoverable, which is
	// the point of storing a hash; there is nothing to return but a new one.
	previousID := s.previousWalletKeyID(internalCtx, db, nsID, wallet)

	// Store the HMAC hash of the key (not the raw key) if HMAC secret is configured.
	//
	// The scope set is written, never left NULL: a key minted with no scopes
	// denies, where an empty column used to be read as admin.
	apiKey, err := NewKey(KeyTypeFor(ownerScopes))
	if err != nil {
		return "", err
	}
	hashedKey := s.HashAPIKey(apiKey)

	// Every key has an expiry now. A key minted by a login is the owner's own
	// key and lives no longer than any other: the point of the column is that
	// no path produces a credential that works forever.
	expiresAt := time.Now().Add(KeyLifetime).UTC().Format(sqliteTime)

	// rotated_from records that this key replaces the wallet's previous one, so
	// `keys list` shows the succession rather than two unrelated keys.
	//
	// The previous key is deliberately NOT revoked. It is very likely deployed
	// somewhere — that is what an owner's key is for — and revoking it because
	// somebody signed in on a laptop would take an application down with no
	// warning. It expires on its own, and `orama namespace keys revoke` ends it
	// sooner when the owner decides to.
	var rotatedFrom interface{}
	if previousID != 0 {
		rotatedFrom = previousID
	}
	inserted, err := db.Query(internalCtx,
		"INSERT INTO api_keys(key, name, namespace_id, scopes, expires_at, rotated_from) VALUES (?, ?, ?, ?, ?, ?)",
		hashedKey, "", nsID, ownerScopes, expiresAt, rotatedFrom,
	)
	if err != nil {
		return "", fmt.Errorf("failed to store api key: %w", err)
	}
	// The id is in the write. Selecting it back reads this node's local
	// SQLite, and a follower has not applied the log yet, so the row is
	// missing and the login that stored the key fails.
	keyID, ok := insertedID(inserted)
	if !ok {
		return "", fmt.Errorf("api key stored for namespace %q but the write returned no id", namespace)
	}

	// Point the wallet at its newest key. REPLACE rather than IGNORE: the row
	// says which key is the wallet's current one, and leaving it pointing at
	// the previous one would make `rotated_from` describe a succession the
	// linkage disagrees with.
	if _, err := db.Query(internalCtx,
		"INSERT OR REPLACE INTO wallet_api_keys(namespace_id, wallet, api_key_id) VALUES (?, ?, ?)",
		nsID, NormalizeWallet(wallet), keyID,
	); err != nil {
		return "", fmt.Errorf("failed to link the api key to wallet in namespace %q: %w", namespace, err)
	}

	// The key belongs to the namespace as well: that grant is how the
	// authorization gate recognizes a request authenticated by the key rather
	// than by the wallet. Without it the caller holds a key that is refused
	// everywhere.
	if err := s.grantServiceAccount(ctx, db, nsID, namespace, hashedKey, RoleForScopes(ownerScopes)); err != nil {
		return "", err
	}

	return apiKey, nil
}

// ResolveNamespaceID returns a namespace's primary key in the cluster registry.
//
// It used to `INSERT OR IGNORE INTO namespaces` first, so resolving a name
// created it — the same create-by-lookup that made `/v1/auth/challenge` a
// namespace-creation endpoint (bug-357). Creating a namespace is
// `POST /v1/namespaces` and nothing else; a name nobody has created does not
// resolve.
//
// It resolves against the registry, not against whatever database this gateway
// happens to hold. Every row it is used to key — a refresh token, a nonce, a
// grant — lives there, so an id from anywhere else would point at a different
// namespace or at nothing.
func (s *Service) ResolveNamespaceID(ctx context.Context, ns string) (interface{}, error) {
	db := s.registryDatabase()
	if db == nil {
		return nil, fmt.Errorf("client not initialized")
	}
	ns = strings.TrimSpace(ns)
	if ns == "" {
		ns = LobbyNamespace
	}

	res, err := db.Query(client.WithInternalAuth(ctx),
		"SELECT id FROM namespaces WHERE name = ? LIMIT 1", ns)
	if err != nil {
		return nil, err
	}
	if res == nil || res.Count == 0 || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return nil, &ErrNoSuchNamespace{Namespace: ns}
	}
	return res.Rows[0][0], nil
}

// ErrNoSuchNamespace names a namespace nobody has created.
type ErrNoSuchNamespace struct{ Namespace string }

func (e *ErrNoSuchNamespace) Error() string {
	return fmt.Sprintf("no such namespace: %q — create it with 'orama namespace create %s'", e.Namespace, e.Namespace)
}

func (s *Service) resolveKeyNamespaceID(ctx context.Context, ns string) (interface{}, error) {
	return s.ResolveNamespaceID(ctx, ns)
}

// Base58Decode decodes a base58-encoded string
func (s *Service) Base58Decode(input string) ([]byte, error) {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	answer := big.NewInt(0)
	j := big.NewInt(1)
	for i := len(input) - 1; i >= 0; i-- {
		tmp := strings.IndexByte(alphabet, input[i])
		if tmp == -1 {
			return nil, fmt.Errorf("invalid base58 character")
		}
		idx := big.NewInt(int64(tmp))
		tmp1 := new(big.Int)
		tmp1.Mul(idx, j)
		answer.Add(answer, tmp1)
		j.Mul(j, big.NewInt(58))
	}
	// Handle leading zeros
	res := answer.Bytes()
	for i := 0; i < len(input) && input[i] == alphabet[0]; i++ {
		res = append([]byte{0}, res...)
	}
	return res, nil
}

// previousWalletKeyID returns the id of the key this wallet was last given in a
// namespace, or 0. It is the `rotated_from` of the key about to be minted.
func (s *Service) previousWalletKeyID(internalCtx context.Context, db client.DatabaseClient, nsID interface{}, wallet string) int64 {
	res, err := db.Query(internalCtx,
		`SELECT api_key_id FROM wallet_api_keys
		  WHERE namespace_id = ? AND wallet = ? LIMIT 1`,
		nsID, NormalizeWallet(wallet))
	if err != nil || res == nil || res.Count == 0 || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0
	}
	return toInt64(res.Rows[0][0])
}
