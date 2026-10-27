/**
 * Device attestation, on the wire.
 *
 * A namespace may admit only its own app on a genuine device. A device-bound
 * session proves that once (App Attest on iOS, key attestation on Android)
 * through `AuthClient.attestIOS` or `attestAndroid`, and the gateway answers
 * with an access token carrying the attestation. The platform makes the proof;
 * the SDK hands it the bytes to sign over and keeps the token it is answered
 * with. See docs/whitepaper/technical-reference/vol1/14-authorization.md,
 * "Device attestation at the gateway".
 */

/** A single-use attestation challenge for the session's device. */
export interface AttestationChallenge {
  challenge: string;
  /** RFC 3339. */
  expires_at: string;
  /** The device the challenge is for: the session's own. */
  device_id: string;
}

/** What an accepted attestation proved, as the gateway shows it. */
export interface AttestationResult {
  platform: "ios" | "android";
  app_id: string;
  app_version: string;
  /** RFC 3339. */
  attested_at: string;
}

/** An App Attest attestation: what `DCAppAttestService.attestKey` returned. */
export interface IOSAttestation {
  challenge: string;
  /** The App Attest key id, standard base64. */
  keyId: string;
  /** The attestation object, standard base64. */
  attestation: string;
}

/** An Android key-attestation certificate chain over the challenge. */
export interface AndroidAttestation {
  challenge: string;
  /** The chain, leaf first, each certificate DER in standard base64. */
  chain: string[];
}

/**
 * What an iOS app signs an App Attest assertion over on a refresh: the
 * refresh proof's id, the device, and `clientDataHash` =
 * SHA-256(proofId ‖ deviceId), which is what `generateAssertion` takes.
 */
export interface AppAttestAssertionRequest {
  proofId: string;
  deviceId: string;
  clientDataHash: Uint8Array;
}

/**
 * Makes the App Attest assertion for one refresh, standard base64, with the
 * platform (`DCAppAttestService.generateAssertion(keyId, clientDataHash)`).
 * Resolve `undefined` to send none (the device has not attested).
 */
export type AppAttestAssertionProvider = (
  request: AppAttestAssertionRequest
) => Promise<string | undefined>;

/**
 * The value a device's platform signs over: SHA-256 of `value` followed by the
 * device id. For an attestation `value` is the challenge (App Attest's
 * clientDataHash on iOS, the key's attestationChallenge on Android); for a
 * refresh's assertion it is the proof's id. The device id is a fixed-length
 * thumbprint, so the concatenation is unambiguous.
 */
export async function attestationBinding(value: string, deviceId: string): Promise<Uint8Array> {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) {
    throw new Error("attestation: this platform has no crypto.subtle to hash the binding with");
  }
  const digest = await subtle.digest("SHA-256", new TextEncoder().encode(value + deviceId));
  return new Uint8Array(digest);
}
