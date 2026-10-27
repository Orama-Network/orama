// A device-bound session through the SDK. It refreshes on a phone whose clock
// is ten minutes ahead, then ten minutes behind (bug-961): the gateway refuses
// a proof outside 60 s of its clock by name and says what its clock reads, and
// the SDK stamps its proofs on the gateway's clock with nothing from the
// application. It attests through the SDK (feat-959): the attest calls carry
// the session's token and their refusals arrive as AttestationErrors, leaving
// the token in place, and a refresh carries the app's App Attest assertion
// over the proof. Then it logs out everywhere (bug-960): the access token
// alone is refused, and the SDK's logout({ all: true }) signs the device's
// proof. A genuine attestation needs a real device and is accepted by hand.
import { createHash, createPrivateKey, sign } from "node:crypto";
import { AttestationError, AuthCode, MemoryStorage } from "../../../../sdk/src/index";
import type { DeviceSigner } from "../../../../sdk/src/index";
import { http, namespace, need, nsClient, nsURL, onFleet, ownerClient } from "./fleet";

const TEN_MINUTES_MS = 10 * 60_000;
/** Revoked tokens stop "within ten seconds on every gateway", plus a round trip. */
const REVOCATION_BOUND_MS = 15_000;
const POLL_MS = 500;

/** The Go runner's device: its Ed25519 private JWK, signing as the platform would. */
function runnerDevice(): DeviceSigner {
  const jwk = JSON.parse(need("E2E_DEVICE_JWK")) as Record<string, string>;
  const key = createPrivateKey({ key: jwk, format: "jwk" });
  return {
    publicJwk: { kty: jwk.kty, crv: jwk.crv, x: jwk.x },
    sign: async (message: string) => sign(null, Buffer.from(message, "utf8"), key).toString("base64url"),
  };
}

/** A client holding the runner's device-bound session, as an app restores one from storage. */
async function deviceClient(storage: MemoryStorage) {
  const client = nsClient({ storage });
  client.auth.setDeviceSigner(runnerDevice());
  return client;
}

describe.skipIf(!onFleet)("sdk device-bound session", () => {
  const storage = new MemoryStorage();

  beforeAll(async () => {
    await storage.set("refreshToken", need("E2E_DEVICE_REFRESH"));
    await storage.set("namespace", namespace());
    await storage.set("deviceId", need("E2E_DEVICE_ID"));
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  for (const [name, skewMs] of [
    ["ten minutes ahead", TEN_MINUTES_MS],
    ["ten minutes behind", -TEN_MINUTES_MS],
  ] as const) {
    it(`a device-bound session refreshes with the phone's clock ${name}`, async () => {
      const realNow = Date.now.bind(Date);
      vi.spyOn(Date, "now").mockImplementation(() => realNow() + skewMs);
      const before = await storage.get("refreshToken");
      const client = await deviceClient(storage);

      const token = await client.auth.refresh();

      expect(token).toBeTruthy();
      expect(await storage.get("refreshToken")).not.toBe(before);
      const who = await client.auth.whoami();
      expect(who.authenticated).toBe(true);
    });
  }

  it("attest calls carry the session's token; a refusal is an AttestationError and the token stays", async () => {
    const client = await deviceClient(storage);
    await client.auth.refresh();
    const before = client.auth.getToken();

    const challenge = await client.auth.attestChallenge();
    expect(challenge.device_id).toBe(need("E2E_DEVICE_ID"));
    expect(challenge.challenge).toBeTruthy();

    // This namespace sets no attestation policy: there is no app to attest against.
    const ios = await client.auth
      .attestIOS({ challenge: challenge.challenge, keyId: "a2V5", attestation: "b2JqZWN0" })
      .catch((e) => e);
    expect(ios).toBeInstanceOf(AttestationError);
    expect(ios.code).toBe(AuthCode.AttestationNotConfigured);
    const android = await client.auth
      .attestAndroid({ challenge: challenge.challenge, chain: ["bGVhZg=="] })
      .catch((e) => e);
    expect(android).toBeInstanceOf(AttestationError);
    expect(android.code).toBe(AuthCode.AttestationNotConfigured);
    expect(client.auth.getToken()).toBe(before);

    // The owner's session is bound to no device: an attestation is a device's.
    const unbound = await ownerClient().auth.attestChallenge().catch((e) => e);
    expect(unbound).toBeInstanceOf(AttestationError);
    expect(unbound.code).toBe(AuthCode.AttestationNeedsDevice);
  });

  it("a refresh asks the app for its App Attest assertion over the refresh proof", async () => {
    const client = await deviceClient(storage);
    const asked: { proofId: string; deviceId: string; clientDataHash: Uint8Array }[] = [];
    client.auth.setAppAttestAssertion(async (request) => {
      asked.push(request);
      return "YXNzZXJ0aW9u";
    });

    expect(await client.auth.refresh()).toBeTruthy();

    expect(asked).toHaveLength(1);
    expect(asked[0].deviceId).toBe(need("E2E_DEVICE_ID"));
    expect(Buffer.from(asked[0].clientDataHash)).toEqual(
      createHash("sha256").update(asked[0].proofId + asked[0].deviceId).digest()
    );
  });

  it("logging out everywhere takes the device's proof: the access token alone is refused, the SDK signs it", async () => {
    const client = await deviceClient(storage);
    const token = await client.auth.refresh();

    await expect(
      http(nsURL(), token).post("/v1/auth/logout", { all: true, namespace: namespace() })
    ).rejects.toMatchObject({ httpStatus: 401, code: "DEVICE_PROOF_REQUIRED" });
    expect((await client.auth.whoami()).authenticated).toBe(true);

    await client.auth.logout({ all: true });

    const deadline = Date.now() + REVOCATION_BOUND_MS;
    for (;;) {
      const refused = await http(nsURL(), token)
        .get("/v1/auth/whoami")
        .then(() => false, (e) => e?.httpStatus === 401);
      if (refused) break;
      if (Date.now() > deadline) throw new Error(`the session's token outlived logout({ all: true }) by ${REVOCATION_BOUND_MS}ms`);
      await new Promise((resolve) => setTimeout(resolve, POLL_MS));
    }
  });
});
