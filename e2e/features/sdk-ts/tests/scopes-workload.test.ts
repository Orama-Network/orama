// The SDK's scope constants against the live gateway, and the workload
// client's refusals outside a deployment, from its own entry
// (@orama-network/orama/workload) and its default Node token reader.
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { KEY_PROFILES, PROFILE_SCOPES, SDKError, isScope } from "../../../../sdk/src/index";
import { createWorkloadClient } from "../../../../sdk/src/workload";
import { http, need, nsURL, onFleet } from "./fleet";

interface MintedKey {
  id: number;
  api_key: string;
  scopes: string;
}

const keysPath = "/v1/namespace/keys";

function scopeSet(stored: string): string[] {
  return stored.split(/[\s,]+/).filter(Boolean).sort();
}

describe.skipIf(!onFleet)("sdk scopes and workload", () => {
  it("every key profile the SDK lists is one the gateway mints, granting exactly the SDK's scopes", async () => {
    const owner = http(nsURL(), need("GATEWAY_JWT"));
    for (const profile of KEY_PROFILES) {
      const key = await owner.post<MintedKey>(keysPath, { scope: profile, label: `e2e-sdk-${profile}` });
      try {
        expect(key.api_key).toBeTruthy();
        expect(scopeSet(key.scopes)).toEqual([...PROFILE_SCOPES[profile]].sort());
        for (const s of scopeSet(key.scopes)) expect(isScope(s)).toBe(true);
      } finally {
        await owner.request("DELETE", `${keysPath}/${key.id}`);
      }
    }
  });

  it("a profile the SDK does not list is refused by the gateway", async () => {
    const owner = http(nsURL(), need("GATEWAY_JWT"));
    await expect(owner.post(keysPath, { scope: "e2e-not-a-profile", label: "e2e" })).rejects.toMatchObject({
      httpStatus: 400,
    });
  });

  it("the workload client refuses to start outside a deployment", async () => {
    await expect(createWorkloadClient({ baseURL: undefined, tokenFile: undefined })).rejects.toMatchObject({
      code: "WORKLOAD_NO_GATEWAY",
    });
    await expect(createWorkloadClient({ baseURL: nsURL() })).rejects.toBeInstanceOf(SDKError);
  });

  it("a workload token file with no token in it is refused, and a forged one cannot reach the gateway", async () => {
    await expect(
      createWorkloadClient({ baseURL: nsURL(), tokenFile: "/token", readFile: async () => "" })
    ).rejects.toBeInstanceOf(SDKError);
    const client = await createWorkloadClient({
      baseURL: nsURL(),
      tokenFile: "/token",
      readFile: async () => "e2e.forged.workload-token",
    }).catch((e) => e);
    if (client instanceof Error) {
      expect(client).toBeInstanceOf(SDKError);
      return;
    }
    const who = await client.auth.whoami();
    expect(who.authenticated).toBe(false);
  });

  it("the workload entry reads its token file from disk by default, and a forged token cannot reach the gateway", async () => {
    const dir = mkdtempSync(join(tmpdir(), "e2e-workload-"));
    try {
      const tokenFile = join(dir, "token");
      writeFileSync(tokenFile, "e2e.forged.workload-token\n");
      const client = await createWorkloadClient({ baseURL: nsURL(), tokenFile });
      expect(client.auth.getToken()).toBe("e2e.forged.workload-token");
      const who = await client.auth.whoami();
      expect(who.authenticated).toBe(false);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
