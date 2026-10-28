import type { Scope } from "./scopes";

/**
 * Every failure the SDK raises is an `SDKError`, so a caller can keep one
 * `catch` and branch on `code`/`httpStatus`. The subclasses below let a caller
 * branch with `instanceof` instead, for the four cases an application actually
 * handles differently: the credential is wrong, the credential is right but too
 * narrow, the thing is not there, and the gateway was never reached.
 *
 * `SDKError.fromResponse` picks the subclass, so existing `catch (e) { if (e
 * instanceof SDKError) … }` code is unaffected.
 */
export class SDKError extends Error {
  public readonly httpStatus: number;
  public readonly code: string;
  public readonly details: Record<string, any>;

  constructor(
    message: string,
    httpStatus: number = 500,
    code: string = "SDK_ERROR",
    details: Record<string, any> = {}
  ) {
    super(message);
    this.name = "SDKError";
    this.httpStatus = httpStatus;
    this.code = code;
    this.details = details;
  }

  /**
   * Build the right error for a gateway response.
   *
   * The gateway answers errors in one of two shapes:
   * - `{error: "message", code?, …}`; anything else in it is kept in
   *   `details`, which is where a structured hint like `required_scope`
   *   arrives.
   * - the RPC envelope `{ok: false, error: {code, message, retryable, …}}`,
   *   whose inner object is unwrapped so `message`, `code` and `retryable`
   *   read the same as the flat shape. Passing the object through as the
   *   message used to produce "[object Object]" and a code of `HTTP_<status>`.
   */
  static fromResponse(status: number, body: any, message?: string): SDKError {
    const envelope =
      body && typeof body.error === "object" && body.error !== null ? body.error : undefined;
    const errorMsg = message || envelope?.message || (envelope ? undefined : body?.error) || `HTTP ${status}`;
    const code = envelope?.code || body?.code || `HTTP_${status}`;
    const details = envelope ?? (body && typeof body === "object" ? body : {});

    // An attestation refusal is about the device and the app, whatever its
    // status: a 403 ATTESTATION_INVALID is not a missing grant, and a 401
    // ATTESTATION_CHALLENGE_INVALID is not a bad credential.
    if (ATTESTATION_CODES.has(code)) {
      return new AttestationError(errorMsg, status, code, details);
    }
    if (status === 401) {
      if (code === AuthCode.Revoked) {
        return new RevokedCredentialError(errorMsg, status, code, details);
      }
      return new AuthError(errorMsg, status, code, details);
    }
    if (status === 403) {
      if (code === AuthCode.NamespaceMismatch) {
        return new NamespaceError(errorMsg, status, code, details);
      }
      if (code === FunctionCode.NotDirectlyInvocable) {
        return new FunctionNotDirectlyInvocableError(errorMsg, status, code, details);
      }
      if (code === AuthCode.DeviceRevoked) {
        return new RevokedDeviceError(errorMsg, status, code, details);
      }
      return new ScopeError(errorMsg, status, code, details);
    }
    if (status === 404) {
      return new NotFoundError(errorMsg, status, code, details);
    }
    return new SDKError(errorMsg, status, code, details);
  }

  /**
   * Whether the gateway said a retry may succeed, when it said. `undefined`
   * means the gateway gave no verdict (an older gateway, or a flat error).
   */
  get retryable(): boolean | undefined {
    const r = this.details?.retryable;
    return typeof r === "boolean" ? r : undefined;
  }

  /**
   * What to do about it, when the gateway said. Every refusal carries one: the
   * code says what happened, the hint says what to do next.
   */
  get hint(): string | undefined {
    return this.details?.hint;
  }

  toJSON() {
    return {
      name: this.name,
      message: this.message,
      httpStatus: this.httpStatus,
      code: this.code,
      details: this.details,
    };
  }
}

/**
 * The codes the gateway puts on a refusal.
 *
 * A 401 had at least six causes and told them apart only by an English string,
 * so nothing could distinguish "you sent nothing" from "your key was revoked"
 * without matching on prose. Switch on `error.code` against these.
 *
 * Mirrors `core/pkg/gateway/auth_errors.go`; the list only ever grows.
 */
export const AuthCode = {
  /** No credential was presented. */
  Missing: "AUTH_MISSING",
  /** A credential was presented and is not one this cluster knows. */
  InvalidKey: "AUTH_INVALID_KEY",
  /**
   * The credential or session was revoked: the token, its session, the key it
   * came from, or every session of its account. Sign in again. A token
   * refused because its device was revoked is `DEVICE_REVOKED` instead.
   */
  Revoked: "AUTH_REVOKED",
  /** The credential expired. Refresh, or sign in again. */
  Expired: "AUTH_EXPIRED",
  /**
   * The gateway cannot tell right now whether this credential was revoked (its
   * revocation list is older than its bound and the registry is not answering).
   * Retryable: the same request may succeed in a moment. Not a refusal of the
   * credential.
   */
  Unavailable: "AUTH_UNAVAILABLE",
  /** The grant is held but the operation needs a logged-in user. */
  UserLoginRequired: "USER_JWT_REQUIRED",
  /** The credential lacks the grant named in `requiredScope`. */
  ScopeMissing: "INSUFFICIENT_SCOPE",
  /** The credential belongs to another namespace. */
  NamespaceMismatch: "NAMESPACE_MISMATCH",
  /** The credential is not an owner of this namespace. */
  OwnershipRequired: "OWNERSHIP_REQUIRED",
  /** A WebSocket upgrade from a page on another site; the gateway refuses it at the edge. */
  OriginNotAllowed: "ORIGIN_NOT_ALLOWED",
  /** The route is the cluster operator's. */
  OperatorRequired: "NOT_AN_OPERATOR",
  /** The destination is refused; a different credential will not help. */
  DestinationNotAllowed: "DESTINATION_NOT_ALLOWED",
  /**
   * The wallet already owns as many namespaces as one wallet may (403 on a namespace create). The
   * message names the cap; deleting a namespace or handing one on makes room.
   */
  NamespaceQuota: "NAMESPACE_QUOTA",
  /**
   * A namespace transfer the recipient cannot take, for example a wallet at its namespace cap (403).
   * Deliberately generic: it names neither the wallet nor the limit. The owner keeps the namespace.
   */
  TransferRefused: "TRANSFER_REFUSED",

  // --- Signing in with a wallet ------------------------------------------
  /** The sign-in message could not be read. Send the one `challenge()` returned, verbatim. */
  MessageMalformed: "AUTH_MESSAGE_MALFORMED",
  /** The message was signed for another host. Ask this gateway for the challenge. */
  DomainMismatch: "AUTH_DOMAIN_MISMATCH",
  /** The message's own expiry has passed. Ask for a new challenge. */
  MessageExpired: "AUTH_MESSAGE_EXPIRED",
  /** The message is fine and the signature over it is not. */
  SignatureInvalid: "AUTH_SIGNATURE_INVALID",
  /**
   * The challenge cannot be claimed: never issued, already used, or expired.
   *
   * One code for three causes on purpose — telling them apart would say which
   * wallets hold outstanding challenges, and the answer is the same in all
   * three: ask for a new one.
   */
  ChallengeInvalid: "AUTH_CHALLENGE_INVALID",

  // --- Sessions bound to a device ------------------------------------------
  /** The namespace requires sessions bound to a device; sign in with a device key. */
  DeviceRequired: "DEVICE_REQUIRED",
  /** The device key is not a P-256 or Ed25519 public JWK, or not the device the message names. */
  DeviceKeyInvalid: "DEVICE_KEY_INVALID",
  /** The device's signature over the sign-in message does not verify. */
  DeviceSignatureInvalid: "DEVICE_SIGNATURE_INVALID",
  /** A device-bound credential was sent without the device's proof. */
  DeviceProofRequired: "DEVICE_PROOF_REQUIRED",
  /** The device proof is malformed, reused, or not the device's. Make a fresh one. */
  DeviceProofInvalid: "DEVICE_PROOF_INVALID",
  /**
   * The device proof's time is outside the window around the gateway's clock:
   * this device's clock is off. `details.server_time` is the gateway's clock
   * (unix seconds). The SDK stamps its proofs on the gateway's clock and makes
   * a refused one again on it, so an application sees this only when the
   * clock moves again between the two; tell the user their clock is wrong
   * rather than signing them out.
   */
  DeviceProofClockSkew: "DEVICE_PROOF_CLOCK_SKEW",
  /**
   * The device was revoked; its key can never hold a session again. Sign-in,
   * refresh, and any request or WebSocket upgrade with a token bound to it
   * answer it (403), with `details.revocation`: who revoked it and what proves
   * it. Check it with `verifyDeviceRevocation` before acting on it. A revoked
   * token, session, key or account is `AUTH_REVOKED` instead.
   */
  DeviceRevoked: "DEVICE_REVOKED",
  /** The device waits for another of the account's devices to approve it. */
  DevicePending: "DEVICE_PENDING",
  /**
   * The session is a pending device's restricted session (the namespace's
   * function device policy) and this call is not open to it: it reaches its
   * own session and the functions deployed `open_to_pending_devices`. Run the
   * application's admission flow; once a function approves the device,
   * `refresh()` and retry. `details.device_id` is the device.
   */
  DevicePendingApproval: "DEVICE_PENDING_APPROVAL",
  /** The account has no such device. */
  DeviceNotFound: "DEVICE_NOT_FOUND",
  /** The device key is enrolled for another account. */
  DeviceKeyTaken: "DEVICE_KEY_TAKEN",
  /** A device-flow user code is not the shape the gateway issues. */
  DeviceUserCodeMalformed: "DEVICE_USER_CODE_MALFORMED",
  /**
   * The pending device-flow login is for another namespace or account, or is
   * the other kind (a device link offered to a wallet, a plain login to a device).
   */
  DeviceLoginMismatch: "DEVICE_LOGIN_MISMATCH",
  /**
   * A device link (starting, approving or collecting one) in a namespace
   * whose own functions admit its users' devices (the function policy).
   */
  DeviceLinkFunctionPolicy: "DEVICE_LINK_FUNCTION_POLICY",
  /** The session policy is set, but revoking existing sign-in keys stopped partway. Repeat the request. */
  PolicySweepIncomplete: "POLICY_SWEEP_INCOMPLETE",
  /**
   * The wallet holds no grant in the namespace (never invited, or the grant was
   * revoked, expired or disabled) and the namespace is not open to other
   * wallets: its session was refused a refresh. Sign in again once it is open,
   * or ask the namespace's owner for an invitation.
   */
  SignInClosed: "SIGN_IN_CLOSED",

  // --- Device attestation --------------------------------------------------
  /**
   * The namespace admits only its own app on an attested device, and this
   * session's device has not attested (or its attestation ran out). Attest
   * (`/v1/attest/challenge`, then `/v1/attest/ios` or `/v1/attest/android`) and
   * use the token it returns.
   */
  AttestationRequired: "ATTESTATION_REQUIRED",
  /** The attestation is of an app, platform, environment or OS the namespace does not admit, or the proof was refused. */
  AttestationInvalid: "ATTESTATION_INVALID",
  /** The build is older than the namespace admits: update the app. */
  AppUpdateRequired: "APP_UPDATE_REQUIRED",
  /** The attestation challenge was never issued to this device, was already used, or expired. Ask for a new one. */
  AttestationChallengeInvalid: "ATTESTATION_CHALLENGE_INVALID",
  /** The namespace's attestation mode is off: there is no app to attest against. */
  AttestationNotConfigured: "ATTESTATION_NOT_CONFIGURED",
  /** The session is bound to no device, or the caller is a key: an attestation is bound to a device. */
  AttestationNeedsDevice: "ATTESTATION_NEEDS_DEVICE",
  /** The attestation request is not one the route reads (fields, base64, chain length, size). */
  AttestationMalformed: "ATTESTATION_MALFORMED",
  /** The device holds as many unanswered challenges as it may. Answer one, or wait for them to expire. */
  AttestationChallengeLimit: "ATTESTATION_CHALLENGE_LIMIT",
  /** An attestation policy that cannot be enforced as written (a namespace operator's PUT). */
  AttestationPolicyInvalid: "ATTESTATION_POLICY_INVALID",
} as const;

export type AuthCode = (typeof AuthCode)[keyof typeof AuthCode];

/** The codes an `AttestationError` carries. */
const ATTESTATION_CODES = new Set<string>([
  AuthCode.AttestationRequired,
  AuthCode.AttestationInvalid,
  AuthCode.AppUpdateRequired,
  AuthCode.AttestationChallengeInvalid,
  AuthCode.AttestationNotConfigured,
  AuthCode.AttestationNeedsDevice,
  AuthCode.AttestationMalformed,
  AuthCode.AttestationChallengeLimit,
  AuthCode.AttestationPolicyInvalid,
]);

/**
 * The namespace admits only its own app on an attested device, and this
 * refusal is about that: the device has not attested (`ATTESTATION_REQUIRED`),
 * its proof was refused (`ATTESTATION_INVALID`), the build is too old
 * (`APP_UPDATE_REQUIRED`), or an attest call could not be served (the
 * challenge, the device, the request, the namespace's mode). Switch on `code`
 * against `AuthCode`.
 */
export class AttestationError extends SDKError {
  constructor(
    message: string,
    httpStatus: number = 403,
    code: string = AuthCode.AttestationRequired,
    details: Record<string, any> = {}
  ) {
    super(message, httpStatus, code, details);
    this.name = "AttestationError";
  }
}

/**
 * The credential was missing, malformed, expired, or is not enough on its own.
 *
 * The gateway also answers 401 when a data-plane grant needs a logged-in user
 * and only an API key was sent; that case carries the code `USER_JWT_REQUIRED`
 * and names the grant in `requiredScope`.
 */
export class AuthError extends SDKError {
  constructor(
    message: string,
    httpStatus: number = 401,
    code: string = "UNAUTHORIZED",
    details: Record<string, any> = {}
  ) {
    super(message, httpStatus, code, details);
    this.name = "AuthError";
  }

  /** The grant the operation needs, when the gateway named one. */
  get requiredScope(): Scope | undefined {
    return this.details?.required_scope;
  }
}

/**
 * The credential is valid but its grants do not cover the operation.
 *
 * `requiredScope` is the grant to ask for. It comes from the gateway's
 * `required_scope` field rather than from parsing the message.
 */
export class ScopeError extends SDKError {
  constructor(
    message: string,
    httpStatus: number = 403,
    code: string = "FORBIDDEN",
    details: Record<string, any> = {}
  ) {
    super(message, httpStatus, code, details);
    this.name = "ScopeError";
  }

  /** The grant the operation needs, when the gateway named one. */
  get requiredScope(): Scope | undefined {
    return this.details?.required_scope;
  }
}

/** The addressed thing does not exist. */
export class NotFoundError extends SDKError {
  constructor(
    message: string,
    httpStatus: number = 404,
    code: string = "NOT_FOUND",
    details: Record<string, any> = {}
  ) {
    super(message, httpStatus, code, details);
    this.name = "NotFoundError";
  }
}

/**
 * No HTTP response was received: DNS failure, connection refused, TLS failure,
 * offline, a timeout, or a caller's abort. `httpStatus` is 0 in every case,
 * which is how "could not reach the gateway" is told apart from a real 4xx/5xx.
 */
export class NetworkError extends SDKError {
  constructor(
    message: string,
    code: string = "NETWORK_ERROR",
    details: Record<string, any> = {}
  ) {
    super(message, 0, code, details);
    this.name = "NetworkError";
  }
}

/**
 * The credential or the session was revoked, so retrying with it will never
 * work. Distinct from every other 401 because the answer is "sign in again"
 * rather than "check what you sent".
 */
/**
 * The credential belongs to a different namespace than the one being reached.
 *
 * Its own class because the fix is never "sign in again" or "ask for more
 * grants": it is that the client is pointed at the wrong gateway, or the key
 * came from the wrong environment. `namespace` is the one the gateway serves;
 * `credentialNamespace` is the one the credential belongs to.
 */
export class NamespaceError extends SDKError {
  constructor(
    message: string,
    httpStatus = 403,
    code: string = AuthCode.NamespaceMismatch,
    details: Record<string, any> = {}
  ) {
    super(message, httpStatus, code, details);
    this.name = "NamespaceError";
  }

  /** The namespace the gateway serves. */
  get namespace(): string | undefined {
    return this.details?.namespace;
  }

  /** The namespace the credential belongs to. */
  get credentialNamespace(): string | undefined {
    return this.details?.credential_namespace;
  }
}

export class RevokedCredentialError extends AuthError {
  constructor(
    message: string,
    httpStatus: number = 401,
    code: string = AuthCode.Revoked,
    details: Record<string, any> = {}
  ) {
    super(message, httpStatus, code, details);
    this.name = "RevokedCredentialError";
  }
}

/**
 * 403 `DEVICE_REVOKED`: the device this client signs in as was revoked, and
 * can never hold a session again. Not a missing grant: renewing or asking for
 * more scopes cannot fix it. `details.revocation` says who revoked it and what
 * proves it; check it with `verifyDeviceRevocation(deviceRevocationOf(err), …)`
 * before erasing anything.
 */
export class RevokedDeviceError extends SDKError {
  constructor(
    message: string,
    httpStatus: number = 403,
    code: string = AuthCode.DeviceRevoked,
    details: Record<string, any> = {}
  ) {
    super(message, httpStatus, code, details);
    this.name = "RevokedDeviceError";
  }
}

/**
 * The codes a function invoke fails with, beside the auth codes above.
 *
 * Mirrors `core/pkg/httputil/rpc_error.go`; the list only ever grows.
 */
export const FunctionCode = {
  /**
   * 429: refused before the function ran (an invoke rate limit). Safe to send
   * again after `Retry-After`.
   */
  RateLimited: "RATE_LIMITED",
  /**
   * 504: the function ran and was stopped at its timeout. What it did before it
   * was stopped may have happened, so the SDK never sends it again on its own;
   * repeat it only if the call is idempotent.
   */
  Timeout: "FUNCTION_TIMEOUT",
  /** 503: its code could not be loaded on this gateway. Safe to send again. */
  Unavailable: "FUNCTION_UNAVAILABLE",
  /** 500: the function ran and returned or raised an error. */
  ExecutionFailed: "FUNCTION_EXECUTION_FAILED",
  /**
   * 404: `name@N` named a version a later deploy or a rollback replaced. Only
   * the current version of a function runs; call it by name, or have the
   * owner roll back to make that version current. Not retryable.
   */
  VersionSuperseded: "FUNCTION_VERSION_SUPERSEDED",
  /**
   * 403: the function declares `invocable_by` and runs only when one of the
   * functions it names calls it with `function_invoke` or
   * `function_invoke_async`. No credential changes that: send the call through
   * the function it names (an app's router). Raised as
   * `FunctionNotDirectlyInvocableError`. Not retryable.
   */
  NotDirectlyInvocable: "FUNCTION_NOT_DIRECTLY_INVOCABLE",
} as const;

export type FunctionCode = (typeof FunctionCode)[keyof typeof FunctionCode];

/**
 * The function may not be invoked directly: it declares `invocable_by`, and
 * only the functions it names may call it. It is not a `ScopeError`, because
 * no grant the caller could ask for opens it; send the call through the
 * function it names.
 */
export class FunctionNotDirectlyInvocableError extends SDKError {
  constructor(
    message: string,
    httpStatus: number = 403,
    code: string = FunctionCode.NotDirectlyInvocable,
    details: Record<string, any> = {}
  ) {
    super(message, httpStatus, code, details);
    this.name = "FunctionNotDirectlyInvocableError";
  }
}

/**
 * The codes of the relayed-fetch path (`storage.fetchWith`).
 *
 * `FetchCap*` come from the storage node when it refuses a fetch capability;
 * `Relay*` and `RateLimited` come from the relay, before or instead of a
 * tunnel. None of them is ever answered by a direct fetch: a relay that fails
 * surfaces the failure, because retrying direct would hand the storage node the
 * caller's address.
 */
export const RelayCode = {
  /** The capability is forged, expired, for another CID or another namespace. */
  FetchCapInvalid: "FETCH_CAP_INVALID",
  /** The capability was revoked by its owner. */
  FetchCapRevoked: "FETCH_CAP_REVOKED",
  /** The relayed route was reached without a capability header. */
  FetchCapMissing: "FETCH_CAP_MISSING",
  /** A revoke by id without the `revokeKey` the mint returned for it, or with another. */
  FetchCapRevokeKeyInvalid: "FETCH_CAP_REVOKE_KEY_INVALID",
  /** The relay refuses this destination (host or port not in its allowlist). */
  DestinationNotAllowed: "RELAY_DESTINATION_NOT_ALLOWED",
  /** The relay's per-address rate limit. `retryAfterSeconds` says when to try again. */
  RateLimited: "RATE_LIMITED",
  /** The relay cannot carry a stream now (for example its anonymity network is down). */
  Unavailable: "RELAY_UNAVAILABLE",
  /** The relay answered with a status this SDK does not know. */
  Refused: "RELAY_REFUSED",
  /** The relay could not be reached, or the tunnel failed (TLS, reset, closed early). */
  ConnectFailed: "RELAY_CONNECT_FAILED",
  /** The storage node's answer through the tunnel was not a well-formed HTTP/1.1 response. */
  ProtocolError: "RELAY_PROTOCOL_ERROR",
  /** The response body passed the size the caller allowed. */
  TooLarge: "RELAY_RESPONSE_TOO_LARGE",
  /** `RelayedFetch` needs Node.js (`node:tls`). */
  UnsupportedRuntime: "RELAY_UNSUPPORTED_RUNTIME",
  /** No relay in the configured set is usable for this namespace host. */
  NoRelay: "RELAY_NONE_CONFIGURED",
} as const;

export type RelayCode = (typeof RelayCode)[keyof typeof RelayCode];

/**
 * A relayed fetch did not complete because of the relay or the tunnel, not
 * because of the storage node's answer. Never retried direct.
 *
 * `httpStatus` is the relay's refusal status when it refused before upgrading
 * the connection, and 0 when no HTTP answer was received.
 */
export class RelayError extends SDKError {
  constructor(
    message: string,
    httpStatus: number = 0,
    code: string = RelayCode.ConnectFailed,
    details: Record<string, any> = {}
  ) {
    super(message, httpStatus, code, details);
    this.name = "RelayError";
  }

  /** Seconds the relay asked the caller to wait (`Retry-After` on a 429). */
  get retryAfterSeconds(): number | undefined {
    const s = this.details?.retry_after_seconds;
    return typeof s === "number" ? s : undefined;
  }
}

/**
 * The storage node refused the fetch capability. Minting a new one (or asking
 * the owner for one) is the only fix: retrying the same token cannot succeed.
 */
export class FetchCapError extends SDKError {
  constructor(
    message: string,
    httpStatus: number = 403,
    code: string = RelayCode.FetchCapInvalid,
    details: Record<string, any> = {}
  ) {
    super(message, httpStatus, code, details);
    this.name = "FetchCapError";
  }
}

/**
 * The gateway was not told about a logout.
 *
 * `logout` always clears the local session, so this client is signed out when
 * it is raised. What failed is ending the session on the gateway: its tokens
 * stay valid there until they expire, which on a shared or public device is
 * worth telling the user. `httpStatus` and `code` are the failed request's,
 * and `serverError` is that error itself.
 */
export class LogoutError extends SDKError {
  public readonly serverError: SDKError;

  constructor(serverError: SDKError) {
    super(
      `signed out on this device, but the gateway did not end the session: ${serverError.message}`,
      serverError.httpStatus,
      serverError.code,
      serverError.details
    );
    this.name = "LogoutError";
    this.serverError = serverError;
  }
}
