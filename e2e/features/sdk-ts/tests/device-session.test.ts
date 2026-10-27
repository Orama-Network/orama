// A device-bound session through the SDK. It refreshes on a phone whose clock
// is ten minutes ahead, then ten minutes behind (bug-961): the gateway refuses
// a proof outside 60 s of its clock by name and says what its clock reads, and
// the SDK stamps its proofs on the gateway's clock with nothing from the
// application. Then it logs out everywhere (bug-960): the access token alone
// is refused, and the SDK's logout({ all: true }) signs the device's proof.
import { createPrivateKey, sign } from "node:crypto";
import { MemoryStorage } from "../../../../sdk/src/index";
import type { DeviceSigner } from "../../../../sdk/src/index";
import { http, namespace, need, nsClient, nsURL, onFleet } from "./fleet";

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
