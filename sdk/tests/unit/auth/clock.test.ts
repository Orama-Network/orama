import { describe, expect, it, vi } from 'vitest';
import { AuthClient } from '../../../src/auth/client';
import { MemoryStorage } from '../../../src/auth/types';
import { HttpClient } from '../../../src/core/http';
import { AuthCode, AuthError } from '../../../src/errors';
import type { DeviceSigner } from '../../../src/auth/device';

/**
 * A device proof is good within 60 s of the gateway's clock, and a phone's
 * clock can be minutes off: set by hand, a dead RTC battery, a network serving
 * a wrong time (bug-961). The SDK stamps its proofs on the gateway's clock,
 * which every answer's Date header carries, and a proof refused for its time
 * is made again on the time the refusal carries.
 */

const TEN_MINUTES_MS = 10 * 60_000;
const WINDOW_S = 60;
const DEVICE_ID = 'kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k';

const signer: DeviceSigner = {
  publicJwk: { kty: 'OKP', crv: 'Ed25519', x: '11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo' },
  async sign() {
    return 'c2lnbmVk';
  },
};

/**
 * A gateway whose clock is `skewMs` behind this device's, and which refuses a
 * proof outside its window the way the Go gateway does. It sends a Date header
 * only when `dateHeader` is set, so the refusal's server_time is tested alone.
 */
function gateway(skewMs: number, dateHeader: boolean) {
  const proofs: { iat: number; id: string }[] = [];
  const gatewayNow = () => Date.now() - skewMs;
  const answer = (status: number, body: unknown) => {
    const headers: Record<string, string> = { 'content-type': 'application/json' };
    if (dateHeader) headers.date = new Date(gatewayNow()).toUTCString();
    return new Response(JSON.stringify(body), { status, headers });
  };
  const fetchImpl = vi.fn(async (url: any, init: any) => {
    const path = new URL(String(url)).pathname;
    const body = init?.body ? JSON.parse(init.body) : {};
    if (path === '/v1/auth/whoami') {
      return answer(200, { authenticated: true });
    }
    const proof = body.device_proof;
    proofs.push(proof);
    const skew = Math.abs(Math.floor(gatewayNow() / 1000) - proof.iat);
    if (skew > WINDOW_S) {
      return answer(401, {
        error: `the device proof's time is outside the window: iat is ${skew}s from this gateway's clock`,
        code: AuthCode.DeviceProofClockSkew,
        server_time: Math.floor(gatewayNow() / 1000),
      });
    }
    return answer(200, { access_token: 'a.b.c', refresh_token: 'refresh-2' });
  });
  return { fetchImpl, proofs, gatewayNow };
}

async function deviceSession(fetchImpl: any) {
  const storage = new MemoryStorage();
  await storage.set('refreshToken', 'refresh-1');
  await storage.set('namespace', 'anchat');
  await storage.set('deviceId', DEVICE_ID);
  const http = new HttpClient({ baseURL: 'https://gw.example', maxRetries: 0, fetch: fetchImpl });
  const auth = new AuthClient({ httpClient: http, storage });
  auth.setDeviceSigner(signer);
  return { auth, http };
}

describe('a device whose clock is off', () => {
  for (const [name, skewMs] of [
    ['ten minutes ahead', TEN_MINUTES_MS],
    ['ten minutes behind', -TEN_MINUTES_MS],
  ] as const) {
    it(`refreshes with a clock ${name}, stamping the proof on the gateway's clock from its Date header`, async () => {
      const gw = gateway(skewMs, true);
      const { auth } = await deviceSession(gw.fetchImpl);

      await auth.whoami();
      await auth.refresh();

      expect(gw.proofs).toHaveLength(1);
      expect(Math.abs(gw.proofs[0].iat - Math.floor(gw.gatewayNow() / 1000))).toBeLessThanOrEqual(2);
      expect(auth.getToken()).toBe('a.b.c');
    });

    it(`refreshes with a clock ${name} as its first request, making the refused proof again on the refusal's time`, async () => {
      const gw = gateway(skewMs, false);
      const { auth } = await deviceSession(gw.fetchImpl);

      await auth.refresh();

      expect(gw.proofs).toHaveLength(2);
      expect(gw.proofs[1].id).not.toBe(gw.proofs[0].id);
      expect(Math.abs(gw.proofs[1].iat - Math.floor(gw.gatewayNow() / 1000))).toBeLessThanOrEqual(2);
      expect(auth.getToken()).toBe('a.b.c');
    });
  }

  it('tries the corrected proof once, and raises the clock refusal by its own code if that fails too', async () => {
    const refusal = () =>
      new Response(
        JSON.stringify({ error: 'outside the window', code: AuthCode.DeviceProofClockSkew, server_time: 1 }),
        { status: 401, headers: { 'content-type': 'application/json' } }
      );
    const fetchImpl = vi.fn(async () => refusal());
    const { auth } = await deviceSession(fetchImpl);

    const error = await auth.refresh().catch((e) => e);

    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(error).toBeInstanceOf(AuthError);
    expect(error.code).toBe(AuthCode.DeviceProofClockSkew);
  });

  it('does not make a proof again for any other refusal', async () => {
    const fetchImpl = vi.fn(
      async () =>
        new Response(JSON.stringify({ error: 'already used', code: AuthCode.DeviceProofInvalid }), {
          status: 401,
          headers: { 'content-type': 'application/json' },
        })
    );
    const { auth } = await deviceSession(fetchImpl);

    await expect(auth.refresh()).rejects.toMatchObject({ code: AuthCode.DeviceProofInvalid });
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it('does not renew the session over a refused proof, which would replay the proof', async () => {
    const fetchImpl = vi.fn(
      async () =>
        new Response(JSON.stringify({ error: 'already used', code: AuthCode.DeviceProofInvalid }), {
          status: 401,
          headers: { 'content-type': 'application/json' },
        })
    );
    const { auth } = await deviceSession(fetchImpl);

    await expect(auth.revokeDevice('other-device')).rejects.toMatchObject({ code: AuthCode.DeviceProofInvalid });
    const paths = fetchImpl.mock.calls.map((call: any[]) => new URL(String(call[0])).pathname);
    expect(paths).toEqual(['/v1/auth/devices/other-device']);
  });
});

describe('the gateway clock', () => {
  it('is this clock until the gateway has answered, then the Date header the gateway sent', async () => {
    const gatewayMs = Date.parse('Tue, 27 Oct 2026 08:00:00 GMT');
    const http = new HttpClient({
      baseURL: 'https://gw.example',
      maxRetries: 0,
      fetch: (async () =>
        new Response('{}', {
          status: 200,
          headers: { 'content-type': 'application/json', date: new Date(gatewayMs).toUTCString() },
        })) as any,
    });
    const before = http.serverNow();
    expect(Math.abs(before - Date.now())).toBeLessThan(50);

    await http.get('/v1/health');

    expect(Math.abs(http.serverNow() - gatewayMs)).toBeLessThan(1_000);
  });
});
