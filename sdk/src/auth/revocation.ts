/**
 * Checking a `DEVICE_REVOKED` refusal before acting on it.
 *
 * A client told its device was revoked may erase itself. On the gateway's word
 * alone, a misbehaving gateway could erase a healthy device by saying so, so
 * every `DEVICE_REVOKED` refusal carries `revocation`: who revoked the device
 * and what proves it. {@link verifyDeviceRevocation} checks it with keys the
 * client already trusts. See docs/whitepaper/technical-reference/vol1/13-identity.md,
 * "Revoking a device".
 */

import { deviceIdOf, deviceProofMessage } from "./device";

/**
 * Who revoked the device, as the gateway says. Only `device` comes with a
 * signature the client can check; the others are the gateway's word.
 */
export type RevokedBy = "device" | "account" | "operator" | "function" | "unrecorded";

/**
 * The revoking device's proof: its signature, with `public_key`, over
 * `deviceProofMessage("revoke", namespace, <the revoked device's id>, iat, id)`.
 * `device_id` is the RFC 7638 thumbprint of `public_key`.
 */
export interface DeviceRevocationProof {
  namespace: string;
  device_id: string;
  public_key: Record<string, string>;
  iat: number;
  id: string;
  sig: string;
}

/** The `revocation` field of a `DEVICE_REVOKED` refusal. */
export interface DeviceRevocation {
  /** The revoked device: compare it with this device's own id. */
  device_id: string;
  revoked_at: string | null;
  revoked_by: RevokedBy;
  /** The revoking device's proof; null unless `revoked_by` is `device`. */
  proof: DeviceRevocationProof | null;
  /**
   * What the revoker attached for the application (an account-signed roster
   * entry, for example), verbatim. The gateway never reads it: the application
   * checks it, including that it names this device.
   */
  evidence: string | null;
}

/** Checks a signature over `message` (UTF-8) by `publicJwk`; `signature` is base64url. */
export type RevocationSignatureVerifier = (
  publicJwk: Record<string, string>,
  message: string,
  signature: string
) => Promise<boolean>;

export interface VerifyDeviceRevocationOptions {
  /** This device's id: the revocation must name it. */
  deviceId: string;
  /** The namespace this device signs in to: the proof must be signed for it. */
  namespace: string;
  /**
   * The device ids (thumbprints) the application knows to be the account's,
   * whose revocation of this device it accepts. This device's own id is always
   * trusted: only it holds its key. Without this, a proof from any other
   * device is `untrusted_revoker`, because anyone — a gateway included — can
   * make a key and sign a revoke statement with it.
   */
  trustedDeviceIds?: Iterable<string>;
  /**
   * Verifies a signature; the default uses the platform's WebCrypto
   * (`crypto.subtle`, ECDSA P-256 and Ed25519). A platform without it, such as
   * React Native, passes its own.
   */
  verifySignature?: RevocationSignatureVerifier;
}

/**
 * The verdict on a revocation.
 *
 * - `proven`: a device this client trusts (itself, or one of
 *   `trustedDeviceIds`) signed the revocation of this device, in this
 *   namespace. Act on it.
 * - `unproven`: nothing the client trusts signed it — no proof (`no_proof`:
 *   an operator, a function, a session bound to no device, or a revocation
 *   recorded before the gateway kept proofs), or a valid proof from a device
 *   the client does not know (`untrusted_revoker`). Check `evidence`, or the
 *   application's own records, before acting.
 * - `invalid`: the refusal claims a proof that does not hold: tampered, for
 *   another device, for another namespace, or signed by a key that is not the
 *   device it names. Treat the gateway as misbehaving.
 */
export type DeviceRevocationVerdict =
  | {
      status: "proven";
      revokerDeviceId: string;
      selfRevoked: boolean;
      evidence: string | null;
    }
  | {
      status: "unproven";
      reason: "no_proof" | "untrusted_revoker";
      revokedBy: RevokedBy;
      revokerDeviceId: string | null;
      evidence: string | null;
    }
  | {
      status: "invalid";
      reason: string;
      evidence: string | null;
    };

/**
 * The `revocation` of a `DEVICE_REVOKED` error, or undefined for any other
 * error. A `DEVICE_REVOKED` refusal without one is not from a gateway that
 * keeps revocations: pass undefined to {@link verifyDeviceRevocation}, which
 * reports it `invalid`.
 */
export function deviceRevocationOf(err: unknown): DeviceRevocation | undefined {
  const e = err as { code?: unknown; details?: { revocation?: unknown } } | null;
  if (!e || e.code !== "DEVICE_REVOKED") return undefined;
  const revocation = e.details?.revocation;
  return revocation && typeof revocation === "object" ? (revocation as DeviceRevocation) : undefined;
}

/** Check a `DEVICE_REVOKED` refusal's revocation. See {@link DeviceRevocationVerdict}. */
export async function verifyDeviceRevocation(
  revocation: DeviceRevocation | undefined,
  options: VerifyDeviceRevocationOptions
): Promise<DeviceRevocationVerdict> {
  if (!revocation || typeof revocation !== "object") {
    return { status: "invalid", reason: "the refusal carries no revocation", evidence: null };
  }
  const evidence = typeof revocation.evidence === "string" ? revocation.evidence : null;
  const invalid = (reason: string): DeviceRevocationVerdict => ({ status: "invalid", reason, evidence });
  if (revocation.device_id !== options.deviceId) {
    return invalid("the revocation names another device");
  }
  const proof = revocation.proof;
  if (proof == null) {
    if (revocation.revoked_by === "device") {
      return invalid("a revocation by a device without the device's proof");
    }
    return { status: "unproven", reason: "no_proof", revokedBy: revocation.revoked_by, revokerDeviceId: null, evidence };
  }
  if (revocation.revoked_by !== "device") {
    return invalid(`a revocation by ${String(revocation.revoked_by)} with a device proof`);
  }
  if (
    typeof proof.namespace !== "string" ||
    typeof proof.device_id !== "string" ||
    typeof proof.public_key !== "object" ||
    proof.public_key === null ||
    typeof proof.iat !== "number" ||
    typeof proof.id !== "string" ||
    typeof proof.sig !== "string"
  ) {
    return invalid("the proof is malformed");
  }
  if (proof.namespace.toLowerCase() !== options.namespace.toLowerCase()) {
    return invalid("the proof was signed for another namespace");
  }
  let keyId: string;
  try {
    keyId = await deviceIdOf(proof.public_key);
  } catch {
    return invalid("the proof's key is not a P-256 or Ed25519 public JWK");
  }
  if (keyId !== proof.device_id) {
    return invalid("the proof's key is not the device it names");
  }
  const message = deviceProofMessage("revoke", proof.namespace, revocation.device_id, proof.iat, proof.id);
  const verify = options.verifySignature ?? webCryptoVerify;
  // A key the platform will not import (off the curve, malformed, or an
  // algorithm it lacks, as Ed25519 on an older WebCrypto) throws. The answer
  // to a refusal is a verdict, never an exception the caller did not expect:
  // the proof is not one this device can check, so it proves nothing.
  let verified: boolean;
  try {
    verified = await verify(proof.public_key, message, proof.sig);
  } catch (err) {
    const why = err instanceof Error ? err.message : String(err);
    return invalid(`the proof's signature could not be checked: ${why}`);
  }
  if (!verified) {
    return invalid("the proof's signature does not verify");
  }
  const selfRevoked = proof.device_id === options.deviceId;
  const trusted = selfRevoked || new Set(options.trustedDeviceIds ?? []).has(proof.device_id);
  if (!trusted) {
    return {
      status: "unproven",
      reason: "untrusted_revoker",
      revokedBy: "device",
      revokerDeviceId: proof.device_id,
      evidence,
    };
  }
  return { status: "proven", revokerDeviceId: proof.device_id, selfRevoked, evidence };
}

/** Bytes of a P-256 coordinate and of an Ed25519 key or half-signature. */
const SCALAR_BYTES = 32;

/** The DER tag of an ASN.1 SEQUENCE and INTEGER. */
const DER_SEQUENCE = 0x30;
const DER_INTEGER = 0x02;

/**
 * The default verifier: WebCrypto. P-256 signatures are accepted as the 64-byte
 * `r‖s` and as ASN.1 DER, as the gateway accepts them.
 */
export const webCryptoVerify: RevocationSignatureVerifier = async (publicJwk, message, signature) => {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) {
    throw new Error(
      "verifying a revocation: this platform has no crypto.subtle; pass verifySignature"
    );
  }
  let sig: Uint8Array;
  try {
    sig = fromBase64url(signature);
  } catch {
    return false;
  }
  const data = new TextEncoder().encode(message);
  if (publicJwk.kty === "EC" && publicJwk.crv === "P-256") {
    const raw = sig.length === 2 * SCALAR_BYTES ? sig : derToRaw(sig);
    if (!raw) return false;
    const key = await subtle.importKey(
      "jwk",
      { kty: "EC", crv: "P-256", x: publicJwk.x, y: publicJwk.y, ext: true },
      { name: "ECDSA", namedCurve: "P-256" },
      false,
      ["verify"]
    );
    return subtle.verify({ name: "ECDSA", hash: "SHA-256" }, key, raw as BufferSource, data);
  }
  if (publicJwk.kty === "OKP" && publicJwk.crv === "Ed25519") {
    if (sig.length !== 2 * SCALAR_BYTES) return false;
    const key = await subtle.importKey(
      "jwk",
      { kty: "OKP", crv: "Ed25519", x: publicJwk.x, ext: true },
      { name: "Ed25519" },
      false,
      ["verify"]
    );
    return subtle.verify({ name: "Ed25519" }, key, sig as BufferSource, data);
  }
  return false;
};

/** An ASN.1 DER ECDSA signature as the 64-byte `r‖s`, or null if it is not one. */
function derToRaw(der: Uint8Array): Uint8Array | null {
  if (der.length < 8 || der[0] !== DER_SEQUENCE || der[1] !== der.length - 2) return null;
  const out = new Uint8Array(2 * SCALAR_BYTES);
  let at = 2;
  for (let half = 0; half < 2; half++) {
    if (der[at] !== DER_INTEGER) return null;
    const len = der[at + 1];
    let value = der.subarray(at + 2, at + 2 + len);
    if (value.length !== len) return null;
    while (value.length > SCALAR_BYTES && value[0] === 0) value = value.subarray(1);
    if (value.length > SCALAR_BYTES) return null;
    out.set(value, (half + 1) * SCALAR_BYTES - value.length);
    at += 2 + len;
  }
  return at === der.length ? out : null;
}

function fromBase64url(s: string): Uint8Array {
  if (!/^[A-Za-z0-9_-]*$/.test(s)) throw new Error("not base64url");
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (s.length % 4)) % 4);
  const binary = atob(b64);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}
