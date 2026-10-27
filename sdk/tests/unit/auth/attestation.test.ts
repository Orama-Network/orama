import { createHash } from 'node:crypto';
import { describe, expect, it, vi } from 'vitest';
import { AuthClient } from '../../../src/auth/client';
import { MemoryStorage } from '../../../src/auth/types';
import { attestationBinding } from '../../../src/auth/attestation';
import { HttpClient } from '../../../src/core/http';
import { AttestationError, AuthCode, SDKError } from '../../../src/errors';
import type { DeviceSigner } from '../../../src/auth/device';

/**
 * feat-959: an app attests its device through the SDK, and the SDK keeps the
 * attested token as the session's; an iOS refresh carries the app's App Attest
 * assertion over the refresh proof, so every refreshed token keeps `att`.
 */

const DEVICE_ID = 'kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k';

const signer: DeviceSigner = {
  publicJwk: { kty: 'OKP', crv: 'Ed25519', x: '11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo' },
  async sign() {
    return 'c2lnbmVk';
  },
};

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

function gateway(routes: Record<string, (body: any) => Response>) {
  const requests: { path: string; body: any; authorization?: string }[] = [];
  const fetchImpl = vi.fn(async (url: any, init: any) => {
    const path = new URL(String(url)).pathname;
    const body = init?.body ? JSON.parse(init.body) : undefined;
    requests.push({ path, body, authorization: init?.headers?.Authorization });
    const route = routes[path];
    return route ? route(body) : json(404, { error: 'no route' });
  });
  return { fetchImpl, requests };
}

async function deviceSession(fetchImpl: any) {
  const storage = new MemoryStorage();
  await storage.set('refreshToken', 'dv1_refresh');
  await storage.set('namespace', 'anchat');
  await storage.set('deviceId', DEVICE_ID);
  const http = new HttpClient({ baseURL: 'https://ns-anchat.example', maxRetries: 0, fetch: fetchImpl });
  const auth = new AuthClient({ httpClient: http, storage, jwt: 'session.unattested.token' });
  auth.setDeviceSigner(signer);
  return { auth, http, storage };
}

const attested = {
  access_token: 'session.attested.token',
  token_type: 'Bearer',
  expires_in: 900,
  attestation: { platform: 'ios', app_id: 'TEAM.app', app_version: '', attested_at: '2026-10-27T08:00:00Z' },
};

describe('the attestation binding', () => {
  it('is SHA-256 of the value followed by the device id, as the gateway computes it', async () => {
    const want = createHash('sha256').update('challenge-1' + DEVICE_ID).digest();
    expect(Buffer.from(await attestationBinding('challenge-1', DEVICE_ID))).toEqual(want);
  });
});

describe('attesting through the SDK', () => {
  it('asks for a challenge with the session token', async () => {
    const { fetchImpl, requests } = gateway({
      '/v1/attest/challenge': () => json(200, { challenge: 'c1', expires_at: '2026-10-27T08:05:00Z', device_id: DEVICE_ID }),
    });
    const { auth } = await deviceSession(fetchImpl);

    const challenge = await auth.attestChallenge();

    expect(challenge).toEqual({ challenge: 'c1', expires_at: '2026-10-27T08:05:00Z', device_id: DEVICE_ID });
    expect(requests[0].authorization).toBe('Bearer session.unattested.token');
  });

  it('keeps the token an iOS attestation answers with as the session token, for HTTP and the WebSocket', async () => {
    const { fetchImpl, requests } = gateway({
      '/v1/attest/ios': () => json(200, attested),
      '/v1/auth/whoami': () => json(200, { authenticated: true }),
    });
    const { auth, http, storage } = await deviceSession(fetchImpl);

    const out = await auth.attestIOS({ challenge: 'c1', keyId: 'a2V5', attestation: 'b2JqZWN0' });

    expect(requests[0].body).toEqual({ challenge: 'c1', key_id: 'a2V5', attestation: 'b2JqZWN0' });
    expect(out.attestation.platform).toBe('ios');
    expect(auth.getToken()).toBe('session.attested.token');
    expect(await storage.get('jwt')).toBe('session.attested.token');
    // The token a pub/sub or function WebSocket is opened with (?jwt=).
    expect(await http.ensureCredential()).toBe('session.attested.token');
    await auth.whoami();
    expect(requests[1].authorization).toBe('Bearer session.attested.token');
    // The refresh token is the session's, unchanged: attesting mints no session.
    expect(await storage.get('refreshToken')).toBe('dv1_refresh');
  });

  it('keeps the token an Android attestation answers with', async () => {
    const { fetchImpl, requests } = gateway({
      '/v1/attest/android': () => json(200, { ...attested, attestation: { ...attested.attestation, platform: 'android' } }),
    });
    const { auth } = await deviceSession(fetchImpl);

    await auth.attestAndroid({ challenge: 'c1', chain: ['bGVhZg==', 'cm9vdA=='] });

    expect(requests[0].body).toEqual({ challenge: 'c1', chain: ['bGVhZg==', 'cm9vdA=='] });
    expect(auth.getToken()).toBe('session.attested.token');
  });

  for (const [code, status] of [
    [AuthCode.AttestationInvalid, 403],
    [AuthCode.AppUpdateRequired, 403],
    [AuthCode.AttestationChallengeInvalid, 401],
    [AuthCode.AttestationNeedsDevice, 403],
    [AuthCode.AttestationNotConfigured, 409],
    [AuthCode.AttestationRequired, 403],
  ] as const) {
    it(`raises ${code} as an AttestationError and keeps the session's token`, async () => {
      const { fetchImpl, requests } = gateway({ '/v1/attest/ios': () => json(status, { error: 'refused', code }) });
      const { auth } = await deviceSession(fetchImpl);

      const error = await auth.attestIOS({ challenge: 'c1', keyId: 'a2V5', attestation: 'b2JqZWN0' }).catch((e) => e);

      expect(error).toBeInstanceOf(AttestationError);
      expect(error).toBeInstanceOf(SDKError);
      expect(error.code).toBe(code);
      expect(error.httpStatus).toBe(status);
      expect(auth.getToken()).toBe('session.unattested.token');
      // A refused challenge is not an expired session: nothing is renewed or replayed.
      expect(requests.map((r) => r.path)).toEqual(['/v1/attest/ios']);
    });
  }
});

describe('an iOS refresh carries its App Attest assertion', () => {
  it('asks the app for an assertion over the refresh proof and sends it as device_proof.app_attest', async () => {
    const { fetchImpl, requests } = gateway({
      '/v1/auth/refresh': () => json(200, { access_token: 'refreshed.attested.token', refresh_token: 'dv1_next' }),
    });
    const { auth } = await deviceSession(fetchImpl);
    const asked: { proofId: string; deviceId: string; clientDataHash: Uint8Array }[] = [];
    auth.setAppAttestAssertion(async (request) => {
      asked.push(request);
      return 'YXNzZXJ0aW9u';
    });

    await auth.refresh();

    const proof = requests[0].body.device_proof;
    expect(proof.app_attest).toBe('YXNzZXJ0aW9u');
    expect(asked).toHaveLength(1);
    expect(asked[0].proofId).toBe(proof.id);
    expect(asked[0].deviceId).toBe(DEVICE_ID);
    expect(Buffer.from(asked[0].clientDataHash)).toEqual(createHash('sha256').update(proof.id + DEVICE_ID).digest());
    expect(auth.getToken()).toBe('refreshed.attested.token');
  });

  it('makes a new assertion for the new proof when a proof is made again for its time', async () => {
    let calls = 0;
    const { fetchImpl, requests } = gateway({
      '/v1/auth/refresh': () =>
        calls++ === 0
          ? json(401, { error: 'clock', code: AuthCode.DeviceProofClockSkew, server_time: Math.floor(Date.now() / 1000) })
          : json(200, { access_token: 'refreshed.attested.token' }),
    });
    const { auth } = await deviceSession(fetchImpl);
    const asked: string[] = [];
    auth.setAppAttestAssertion(async ({ proofId }) => {
      asked.push(proofId);
      return `assertion-for-${proofId}`;
    });

    await auth.refresh();

    expect(asked).toEqual([requests[0].body.device_proof.id, requests[1].body.device_proof.id]);
    expect(requests[1].body.device_proof.app_attest).toBe(`assertion-for-${requests[1].body.device_proof.id}`);
  });

  it('sends no assertion when the app has none to give, or sets no provider (Android)', async () => {
    const { fetchImpl, requests } = gateway({ '/v1/auth/refresh': () => json(200, { access_token: 'a.b.c' }) });
    const { auth } = await deviceSession(fetchImpl);

    await auth.refresh();
    auth.setAppAttestAssertion(async () => undefined);
    await auth.refresh();

    expect(requests[0].body.device_proof.app_attest).toBeUndefined();
    expect(requests[1].body.device_proof.app_attest).toBeUndefined();
  });

  it('asks for no assertion on any proof but a refresh', async () => {
    const { fetchImpl, requests } = gateway({ '/v1/auth/devices/other': () => json(200, { status: 'revoked' }) });
    const { auth } = await deviceSession(fetchImpl);
    const provider = vi.fn(async () => 'YXNzZXJ0aW9u');
    auth.setAppAttestAssertion(provider);

    await auth.revokeDevice('other');

    expect(provider).not.toHaveBeenCalled();
    expect(requests[0].body.device_proof.app_attest).toBeUndefined();
  });
});
