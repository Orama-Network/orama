// A device-bound session refreshed through the SDK on a phone whose clock is
// ten minutes ahead, then ten minutes behind (bug-961). The gateway refuses a
// proof outside 60 s of its clock by name and says what its clock reads; the
// SDK stamps its proofs on the gateway's clock, so the session refreshes with
// nothing from the application.
import { createPrivateKey, sign } from "node:crypto";
import { MemoryStorage } from "../../../../sdk/src/index";
import type { DeviceSigner } from "../../../../sdk/src/index";
import { namespace, need, nsClient, onFleet } from "./fleet";

const TEN_MINUTES_MS = 10 * 60_000;

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

describe.skipIf(!onFleet)("sdk device proofs on a skewed clock", () => {
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
});
