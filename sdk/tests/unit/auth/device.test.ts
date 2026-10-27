import { describe, expect, it, vi } from 'vitest';
import { AuthClient } from '../../../src/auth/client';
import { MemoryStorage } from '../../../src/auth/types';
import { HttpClient } from '../../../src/core/http';
import { LogoutError } from '../../../src/errors';
import {
  DeviceSigner,
  deviceIdOf,
  deviceProofMessage,
  makeDeviceProof,
  normalizeUserCode,
} from '../../../src/auth/device';

/** A gateway stub that records each request and answers from a table. */
function gateway(routes: Record<string, (body: any) => Response>) {
  const requests: { method: string; path: string; body: any }[] = [];
  const fetchImpl = vi.fn(async (url: any, init: any) => {
    const path = new URL(String(url)).pathname;
    const body = init?.body ? JSON.parse(init.body) : undefined;
    requests.push({ method: init?.method ?? 'GET', path, body });
    const route = routes[path];
    if (!route) return json(404, { error: 'no route' });
    return route(body);
  });
  return { fetchImpl, requests };
}

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function build(fetchImpl: any, storage = new MemoryStorage()) {
  const http = new HttpClient({ baseURL: 'https://gw.example', maxRetries: 0, fetch: fetchImpl });
  return { auth: new AuthClient({ httpClient: http, storage }), storage };
}

/** A signer that records what it was asked to sign. */
function recordingSigner(): DeviceSigner & { signed: string[] } {
  const signed: string[] = [];
  return {
    signed,
    publicJwk: { kty: 'OKP', crv: 'Ed25519', x: '11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo' },
    async sign(message: string) {
      signed.push(message);
      return 'c2lnbmVk';
    },
  };
}

const session = {
  access_token: 'header.payload.sig',
  refresh_token: 'refresh-1',
  subject: '0xwallet',
  namespace: 'anchat',
  device_id: 'kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k',
};

describe('device proofs', () => {
  // The same bytes the gateway checks (authsvc.DeviceProofMessage). A drift of
  // one character is a refresh that never verifies.
  it('signs exactly the statement the gateway verifies', () => {
    expect(deviceProofMessage('refresh', 'anchat', 'refresh-1', 1800000000, 'abcdefghijklmnop')).toBe(
      'orama-device-proof-v1\nrefresh\nanchat\nrefresh-1\n1800000000\nabcdefghijklmnop'
    );
  });

  it('makes a fresh id and the time in seconds, and signs over them', async () => {
    const signer = recordingSigner();
    const a = await makeDeviceProof(signer, 'claim', 'anchat', 'code', 1_800_000_000_999);
    const b = await makeDeviceProof(signer, 'claim', 'anchat', 'code', 1_800_000_000_999);
    expect(a.iat).toBe(1_800_000_000);
    expect(a.id).toMatch(/^[A-Za-z0-9_-]{16,128}$/);
    expect(a.id).not.toBe(b.id);
    expect(signer.signed[0]).toBe(deviceProofMessage('claim', 'anchat', 'code', a.iat, a.id));
  });

  it('normalises a user code the way the gateway stores it', () => {
    expect(normalizeUserCode('bcdf ghjk')).toBe('BCDF-GHJK');
    expect(() => normalizeUserCode('short')).toThrow();
  });
});

describe('device-bound sign-in', () => {
  it('sends the device key and keeps which device the session is bound to', async () => {
    const { fetchImpl, requests } = gateway({ '/v1/auth/verify': () => json(200, session) });
    const { auth, storage } = build(fetchImpl);

    const out = await auth.verify({
      message: 'msg',
      signature: 'sig',
      device_key: recordingSigner().publicJwk,
      device_signature: 'dev-sig',
      device_label: 'phone',
    });

    expect(requests[0].body).toMatchObject({ device_signature: 'dev-sig', device_label: 'phone' });
    expect(requests[0].body.device_key.crv).toBe('Ed25519');
    expect((out as any).device_id).toBe(session.device_id);
    expect(await storage.get('deviceId')).toBe(session.device_id);
    expect(auth.getToken()).toBe(session.access_token);
  });

  it('holds no session while the device waits for approval', async () => {
    const pending = { status: 'pending_approval', device_code: 'dc', user_code: 'BCDF-GHJK', device_id: 'd' };
    const { fetchImpl } = gateway({ '/v1/auth/verify': () => json(202, pending) });
    const { auth, storage } = build(fetchImpl);

    const out = await auth.verify({ message: 'm', signature: 's', device_key: { kty: 'OKP' }, device_signature: 'x' });

    expect(out).toMatchObject({ status: 'pending_approval', user_code: 'BCDF-GHJK' });
    expect(auth.getToken()).toBeUndefined();
    expect(await storage.get('refreshToken')).toBeNull();
  });
});

describe('refreshing a device-bound session', () => {
  it('signs a refresh proof over the refresh token', async () => {
    const { fetchImpl, requests } = gateway({
      '/v1/auth/refresh': () => json(200, { access_token: 'a.b.c', refresh_token: 'refresh-2' }),
    });
    const storage = new MemoryStorage();
    await storage.set('refreshToken', 'refresh-1');
    await storage.set('namespace', 'anchat');
    await storage.set('deviceId', session.device_id);
    const { auth } = build(fetchImpl, storage);
    const signer = recordingSigner();
    auth.setDeviceSigner(signer);

    await auth.refresh();

    const proof = requests[0].body.device_proof;
    expect(proof.sig).toBe('c2lnbmVk');
    expect(signer.signed[0]).toBe(deviceProofMessage('refresh', 'anchat', 'refresh-1', proof.iat, proof.id));
    expect(await storage.get('refreshToken')).toBe('refresh-2');
  });

  it('refuses to refresh without the device key rather than send a refresh the gateway will refuse', async () => {
    const { fetchImpl, requests } = gateway({});
    const storage = new MemoryStorage();
    await storage.set('refreshToken', 'refresh-1');
    await storage.set('deviceId', session.device_id);
    const { auth } = build(fetchImpl, storage);

    await expect(auth.refresh()).rejects.toMatchObject({ code: 'DEVICE_PROOF_REQUIRED' });
    expect(requests).toHaveLength(0);
  });

  it('sends no proof for a session bound to no device', async () => {
    const { fetchImpl, requests } = gateway({
      '/v1/auth/refresh': () => json(200, { access_token: 'a.b.c' }),
    });
    const storage = new MemoryStorage();
    await storage.set('refreshToken', 'refresh-1');
    const { auth } = build(fetchImpl, storage);

    await auth.refresh();
    expect(requests[0].body.device_proof).toBeUndefined();
  });
});

describe('managing devices', () => {
  it('lists and revokes', async () => {
    const { fetchImpl, requests } = gateway({
      '/v1/auth/devices': () => json(200, { devices: [{ id: 'd1', state: 'active', current: true }] }),
      '/v1/auth/devices/d1': () => json(200, { status: 'revoked', id: 'd1' }),
    });
    const { auth } = build(fetchImpl);
    auth.setJwt('a.b.c');

    expect(await auth.listDevices()).toEqual([{ id: 'd1', state: 'active', current: true }]);
    await auth.revokeDevice('d1');
    expect(requests[1]).toMatchObject({ method: 'DELETE', path: '/v1/auth/devices/d1' });
  });

  it('approves a link with a proof over the normalised user code', async () => {
    const { fetchImpl, requests } = gateway({ '/v1/auth/devices/approve': () => json(200, { status: 'approved' }) });
    const storage = new MemoryStorage();
    await storage.set('namespace', 'anchat');
    const { auth } = build(fetchImpl, storage);
    auth.setJwt('a.b.c');
    const signer = recordingSigner();
    auth.setDeviceSigner(signer);

    await auth.approveDeviceLink('bcdfghjk');

    const { user_code, device_proof } = requests[0].body;
    expect(user_code).toBe('BCDF-GHJK');
    expect(signer.signed[0]).toBe(deviceProofMessage('approve', 'anchat', 'BCDF-GHJK', device_proof.iat, device_proof.id));
  });

  it('links a new device without a wallet and collects its session', async () => {
    const { fetchImpl, requests } = gateway({
      '/v1/auth/device': () => json(200, { device_code: 'dc', user_code: 'BCDF-GHJK', device_id: 'd2', expires_in: 600, interval: 5 }),
      '/v1/auth/device/token': () => json(200, { ...session, device_id: 'd2' }),
    });
    const { auth, storage } = build(fetchImpl);
    const signer = recordingSigner();
    auth.setDeviceSigner(signer);

    const link = await auth.startDeviceLink({ namespace: 'anchat', label: 'laptop' });
    expect(requests[0].body).toMatchObject({ namespace: 'anchat', device_label: 'laptop', device_key: signer.publicJwk });

    await auth.claimDeviceLink(link.device_code, 'anchat');
    const proof = requests[1].body.device_proof;
    expect(signer.signed[0]).toBe(deviceProofMessage('claim', 'anchat', 'dc', proof.iat, proof.id));
    expect(await storage.get('deviceId')).toBe('d2');
    expect(auth.getToken()).toBe(session.access_token);
  });

  it('proves the device when a device-bound session revokes another device', async () => {
    const { fetchImpl, requests } = gateway({ '/v1/auth/devices/d2': () => json(200, { status: 'revoked' }) });
    const storage = new MemoryStorage();
    await storage.set('namespace', 'anchat');
    await storage.set('deviceId', 'd1');
    const { auth } = build(fetchImpl, storage);
    auth.setJwt('a.b.c');
    const signer = recordingSigner();
    auth.setDeviceSigner(signer);

    await auth.revokeDevice('d2');
    const proof = requests[0].body.device_proof;
    expect(signer.signed[0]).toBe(deviceProofMessage('revoke', 'anchat', 'd2', proof.iat, proof.id));
  });

  // Logging out on one phone must not sign the account out of the others.
  it('logs a device-bound session out of that device only', async () => {
    const { fetchImpl, requests } = gateway({ '/v1/auth/logout': () => json(200, { status: 'ok' }) });
    const storage = new MemoryStorage();
    await storage.set('refreshToken', 'dv1_refresh');
    await storage.set('namespace', 'anchat');
    await storage.set('deviceId', 'd1');
    const { auth } = build(fetchImpl, storage);
    auth.setJwt('a.b.c');

    await auth.logout();
    expect(requests[0].body).toEqual({ refresh_token: 'dv1_refresh', namespace: 'anchat' });
  });

  // bug-960: from a device-bound session, logging out everywhere is the
  // device's act, signed as revoking a device is.
  it('signs a logout-all proof over its own device id when a device-bound session logs out everywhere', async () => {
    const { fetchImpl, requests } = gateway({ '/v1/auth/logout': () => json(200, { status: 'ok' }) });
    const storage = new MemoryStorage();
    await storage.set('refreshToken', 'dv1_refresh');
    await storage.set('namespace', 'anchat');
    await storage.set('deviceId', 'd1');
    const { auth } = build(fetchImpl, storage);
    auth.setJwt('a.b.c');
    const signer = recordingSigner();
    auth.setDeviceSigner(signer);

    await auth.logout({ all: true });

    const { device_proof, ...rest } = requests[0].body;
    expect(rest).toEqual({ all: true, namespace: 'anchat', refresh_token: 'dv1_refresh' });
    expect(signer.signed[0]).toBe(deviceProofMessage('logout-all', 'anchat', 'd1', device_proof.iat, device_proof.id));
    expect(auth.getToken()).toBeUndefined();
  });

  it('clears the local session and says the gateway was not told when logging out everywhere without the device key', async () => {
    const { fetchImpl, requests } = gateway({ '/v1/auth/logout': () => json(200, { status: 'ok' }) });
    const storage = new MemoryStorage();
    await storage.set('refreshToken', 'dv1_refresh');
    await storage.set('deviceId', 'd1');
    const { auth } = build(fetchImpl, storage);
    auth.setJwt('a.b.c');

    const failure = await auth.logout({ all: true }).catch((e) => e);

    expect(failure).toBeInstanceOf(LogoutError);
    expect(failure.code).toBe('DEVICE_PROOF_REQUIRED');
    expect(requests).toHaveLength(0);
    expect(auth.getToken()).toBeUndefined();
  });

  it('ends every session without a proof from a session bound to no device, as it always has', async () => {
    const { fetchImpl, requests } = gateway({ '/v1/auth/logout': () => json(200, { status: 'ok' }) });
    const { auth } = build(fetchImpl);
    auth.setJwt('a.b.c');

    await auth.logout({ all: true });
    expect(requests[0].body).toEqual({ all: true });
  });

  // RFC 8037 appendix A.3: the gateway computes the same thumbprint.
  it('computes the device id the gateway computes', async () => {
    expect(await deviceIdOf({ kty: 'OKP', crv: 'Ed25519', x: '11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo' })).toBe(
      'kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k'
    );
    await expect(deviceIdOf({ kty: 'RSA' })).rejects.toThrow();
  });

  it('cannot link without a device key', async () => {
    const { fetchImpl } = gateway({});
    const { auth } = build(fetchImpl);
    await expect(auth.startDeviceLink({ namespace: 'anchat' })).rejects.toMatchObject({ code: 'DEVICE_PROOF_REQUIRED' });
  });
});
