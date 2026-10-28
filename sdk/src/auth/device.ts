/**
 * Device-bound sessions, on the wire.
 *
 * A session may be bound to a device: a key pair one installation holds —
 * ECDSA P-256 (iOS Secure Enclave, Android StrongBox, WebCrypto) or Ed25519.
 * The SDK does not hold or generate that key; the platform does, and the
 * application hands the SDK a {@link DeviceSigner} that signs with it. What the
 * SDK does is put the right bytes in front of the signer and the right fields
 * on the request. See docs/whitepaper/technical-reference/vol1/13-identity.md, "Devices".
 */

/** An installation's device key, as the application holds it. */
export interface DeviceSigner {
  /** The device's public JWK: `{kty:"EC",crv:"P-256",x,y}` or `{kty:"OKP",crv:"Ed25519",x}`. */
  publicJwk: Record<string, string>;
  /**
   * Sign the UTF-8 bytes of `message` with the device key and return the
   * signature as base64url. For P-256 either the 64-byte `r‖s` form (WebCrypto,
   * CryptoKit) or ASN.1 DER (Android Keystore, SecKey) is accepted.
   */
  sign(message: string): Promise<string>;
}

/** What a device proof is for. Each is a different signed statement. */
export type DeviceProofAction = "refresh" | "approve" | "claim" | "revoke" | "end-session" | "logout-all";

/** A device's signature over one action, as the gateway reads it. */
export interface DeviceProof {
  iat: number;
  id: string;
  sig: string;
  /**
   * On a refresh from an iOS device in a namespace that attests: the App
   * Attest assertion over SHA-256(id ‖ device id), standard base64.
   */
  app_attest?: string;
}

/** The first line of every proof statement. */
const DEVICE_PROOF_VERSION = "orama-device-proof-v1";

/** Random bytes in a proof id: 128 bits, as base64url. */
const DEVICE_PROOF_ID_BYTES = 16;

/**
 * The exact text a device signs for a proof: the action, the namespace, the
 * credential it travels with (the refresh token, the user code, the device
 * code), the time in unix seconds and a single-use id, one per line.
 */
export function deviceProofMessage(
  action: DeviceProofAction,
  namespace: string,
  binding: string,
  iat: number,
  id: string
): string {
  return [DEVICE_PROOF_VERSION, action, namespace, binding, String(iat), id].join("\n");
}

/**
 * Make a fresh proof with the signer. The id comes from the platform's secure
 * random source; a platform without one cannot make a proof, and says so.
 */
export async function makeDeviceProof(
  signer: DeviceSigner,
  action: DeviceProofAction,
  namespace: string,
  binding: string,
  nowMs: number = Date.now()
): Promise<DeviceProof> {
  const random = globalThis.crypto?.getRandomValues;
  if (typeof random !== "function") {
    throw new Error(
      "device proof: this platform has no crypto.getRandomValues to draw a proof id from"
    );
  }
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(DEVICE_PROOF_ID_BYTES));
  const id = base64url(bytes);
  const iat = Math.floor(nowMs / 1000);
  const sig = await signer.sign(deviceProofMessage(action, namespace, binding, iat, id));
  return { iat, id, sig };
}

/**
 * The device id of a public JWK: its RFC 7638 thumbprint, which is what a
 * challenge names. Computed with the platform's SHA-256 (WebCrypto).
 */
export async function deviceIdOf(publicJwk: Record<string, string>): Promise<string> {
  let canonical: string;
  if (publicJwk.kty === "EC" && publicJwk.crv === "P-256") {
    canonical = `{"crv":"P-256","kty":"EC","x":"${publicJwk.x}","y":"${publicJwk.y}"}`;
  } else if (publicJwk.kty === "OKP" && publicJwk.crv === "Ed25519") {
    canonical = `{"crv":"Ed25519","kty":"OKP","x":"${publicJwk.x}"}`;
  } else {
    throw new Error(`device key: kty ${publicJwk.kty} crv ${publicJwk.crv} is not P-256 or Ed25519`);
  }
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) {
    throw new Error("device id: this platform has no crypto.subtle to hash the key with");
  }
  const digest = await subtle.digest("SHA-256", new TextEncoder().encode(canonical));
  return base64url(new Uint8Array(digest));
}

/**
 * A user code as the gateway stores it — upper case, the separator in the
 * middle — which is what an approving device signs over.
 */
export function normalizeUserCode(raw: string): string {
  const cleaned = raw.toUpperCase().replace(/[^A-Z0-9]/g, "");
  if (cleaned.length !== 8) {
    throw new Error(`a device user code is 8 characters (got ${cleaned.length})`);
  }
  return `${cleaned.slice(0, 4)}-${cleaned.slice(4)}`;
}

/** One of an account's devices, from `GET /v1/auth/devices`. */
export interface DeviceInfo {
  id: string;
  label: string;
  state: "pending" | "active" | "revoked";
  approved_by: string;
  created_at: string | null;
  activated_at: string | null;
  revoked_at: string | null;
  /** Whether this is the device the calling session is bound to. */
  current: boolean;
}

/**
 * A sign-in whose device must be approved from another of the account's
 * devices first (`202`, the approval device policy). Show `user_code` there;
 * then collect the session with `claimDeviceLink(device_code, namespace)`.
 * Under the function policy a later device gets a session at once instead,
 * with `device_state: "pending"` (see `AuthClient.deviceState()`).
 */
export interface PendingDeviceApproval {
  status: "pending_approval";
  code: string;
  device_id: string;
  device_code: string;
  user_code: string;
  expires_in: number;
  interval: number;
  subject: string;
  namespace: string;
}

/** A pending device link, from `startDeviceLink`. */
export interface DeviceLink {
  device_code: string;
  user_code: string;
  device_id: string;
  expires_in: number;
  interval: number;
}

function base64url(bytes: Uint8Array): string {
  let binary = "";
  bytes.forEach((b) => {
    binary += String.fromCharCode(b);
  });
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
