import { describe, expect, it, vi } from 'vitest';
import { AuthClient } from '../../../src/auth/client';
import { MemoryStorage } from '../../../src/auth/types';
import { HttpClient } from '../../../src/core/http';
import { DeviceSigner, deviceIdOf, deviceProofMessage } from '../../../src/auth/device';
import {
  DeviceRevocation,
  deviceRevocationOf,
  verifyDeviceRevocation,
} from '../../../src/auth/revocation';

const NAMESPACE = 'anchat';

function b64url(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('base64url');
}

/** A device whose key lives in WebCrypto, as a browser's or Node's would. */
async function device(alg: 'P-256' | 'Ed25519', der = false) {
  const params = alg === 'P-256' ? { name: 'ECDSA', namedCurve: 'P-256' } : { name: 'Ed25519' };
  const pair = (await crypto.subtle.generateKey(params, true, ['sign', 'verify'])) as CryptoKeyPair;
  const jwk = await crypto.subtle.exportKey('jwk', pair.publicKey);
  const publicJwk: Record<string, string> =
    alg === 'P-256'
      ? { kty: 'EC', crv: 'P-256', x: jwk.x!, y: jwk.y! }
      : { kty: 'OKP', crv: 'Ed25519', x: jwk.x! };
  const signer: DeviceSigner = {
    publicJwk,
    async sign(message) {
      const algo = alg === 'P-256' ? { name: 'ECDSA', hash: 'SHA-256' } : { name: 'Ed25519' };
      const raw = new Uint8Array(await crypto.subtle.sign(algo, pair.privateKey, new TextEncoder().encode(message)));
      return b64url(der ? toDer(raw) : raw);
    },
  };
  return { signer, id: await deviceIdOf(publicJwk) };
}

/** r‖s as ASN.1 DER, the way Android Keystore and SecKey sign. */
function toDer(raw: Uint8Array): Uint8Array {
  const int = (v: Uint8Array) => {
    let i = 0;
    while (i < v.length - 1 && v[i] === 0) i++;
    let out = v.subarray(i);
    if (out[0] & 0x80) out = Uint8Array.from([0, ...out]);
    return [0x02, out.length, ...out];
  };
  const body = [...int(raw.subarray(0, 32)), ...int(raw.subarray(32))];
  return Uint8Array.from([0x30, body.length, ...body]);
}

/** The revocation a gateway shows `revoked` when `revoker` revoked it. */
async function revokedBy(
  revoker: { signer: DeviceSigner; id: string },
  revokedId: string,
  namespace = NAMESPACE
): Promise<DeviceRevocation> {
  const iat = 1_800_000_000;
  const id = 'proof-id-0123456789';
  const sig = await revoker.signer.sign(deviceProofMessage('revoke', namespace, revokedId, iat, id));
  return {
    device_id: revokedId,
    revoked_at: '2026-10-28T10:00:00Z',
    revoked_by: 'device',
    proof: { namespace, device_id: revoker.id, public_key: revoker.signer.publicJwk, iat, id, sig },
    evidence: 'roster-link',
  };
}

describe('verifyDeviceRevocation', () => {
  it('proves a revocation signed by a trusted device, P-256 raw and DER, and Ed25519', async () => {
    const me = await device('Ed25519');
    for (const revoker of [await device('P-256'), await device('P-256', true), await device('Ed25519')]) {
      const verdict = await verifyDeviceRevocation(await revokedBy(revoker, me.id), {
        deviceId: me.id,
        namespace: NAMESPACE,
        trustedDeviceIds: [revoker.id],
      });
      expect(verdict).toEqual({ status: 'proven', revokerDeviceId: revoker.id, selfRevoked: false, evidence: 'roster-link' });
    }
  });

  it('proves a device that revoked itself without being told whom to trust', async () => {
    const me = await device('P-256');
    const verdict = await verifyDeviceRevocation(await revokedBy(me, me.id), { deviceId: me.id, namespace: NAMESPACE });
    expect(verdict).toMatchObject({ status: 'proven', selfRevoked: true });
  });

  it('does not take a valid proof from an unknown device as proof', async () => {
    // A gateway can make a key of its own and sign a revoke statement with it.
    const me = await device('Ed25519');
    const stranger = await device('Ed25519');
    const verdict = await verifyDeviceRevocation(await revokedBy(stranger, me.id), { deviceId: me.id, namespace: NAMESPACE });
    expect(verdict).toMatchObject({ status: 'unproven', reason: 'untrusted_revoker', revokerDeviceId: stranger.id });
  });

  it('reports a revocation with no proof as unproven, with its evidence', async () => {
    const me = await device('Ed25519');
    for (const by of ['operator', 'function', 'account', 'unrecorded'] as const) {
      const verdict = await verifyDeviceRevocation(
        { device_id: me.id, revoked_at: null, revoked_by: by, proof: null, evidence: by === 'operator' ? 'x' : null },
        { deviceId: me.id, namespace: NAMESPACE }
      );
      expect(verdict).toEqual({
        status: 'unproven',
        reason: 'no_proof',
        revokedBy: by,
        revokerDeviceId: null,
        evidence: by === 'operator' ? 'x' : null,
      });
    }
  });

  it('refuses a tampered proof, one for another device or namespace, and a key that is not its device', async () => {
    const me = await device('Ed25519');
    const other = await device('Ed25519');
    const revoker = await device('P-256');
    const opts = { deviceId: me.id, namespace: NAMESPACE, trustedDeviceIds: [revoker.id] };
    const good = await revokedBy(revoker, me.id);

    const cases: Record<string, DeviceRevocation | undefined> = {
      'another device': await revokedBy(revoker, other.id),
      'another device, relabelled': { ...(await revokedBy(revoker, other.id)), device_id: me.id },
      'another namespace': await revokedBy(revoker, me.id, 'elsewhere'),
      'another namespace, relabelled': { ...good, proof: { ...good.proof!, namespace: 'other' } },
      'a moved iat': { ...good, proof: { ...good.proof!, iat: good.proof!.iat + 1 } },
      'a flipped signature': {
        ...good,
        proof: { ...good.proof!, sig: (good.proof!.sig[0] === 'A' ? 'B' : 'A') + good.proof!.sig.slice(1) },
      },
      'a key that is not its device': { ...good, proof: { ...good.proof!, device_id: other.id } },
      'another key under its id': { ...good, proof: { ...good.proof!, public_key: other.signer.publicJwk } },
      'a device revocation with no proof': { ...good, proof: null },
      'an operator revocation with a proof': { ...good, revoked_by: 'operator' },
      'no revocation at all': undefined,
    };
    for (const [name, revocation] of Object.entries(cases)) {
      const verdict = await verifyDeviceRevocation(revocation, opts);
      expect(verdict.status, name).toBe('invalid');
    }
    expect((await verifyDeviceRevocation(good, opts)).status).toBe('proven');
  });

  it('uses the verifier it is given', async () => {
    const me = await device('Ed25519');
    const revoker = await device('Ed25519');
    const verifySignature = vi.fn(async () => false);
    const verdict = await verifyDeviceRevocation(await revokedBy(revoker, me.id), {
      deviceId: me.id,
      namespace: NAMESPACE,
      trustedDeviceIds: [revoker.id],
      verifySignature,
    });
    expect(verdict.status).toBe('invalid');
    expect(verifySignature).toHaveBeenCalledOnce();
  });

  /**
   * feat-976 review: a key WebCrypto will not import (a point off the curve)
   * or a verifier that fails (a platform without Ed25519) threw out of the
   * check instead of answering it.
   */
  it('answers invalid, not an exception, for a key it cannot check', async () => {
    const me = await device('Ed25519');
    const offCurve = { kty: 'EC', crv: 'P-256', x: b64url(new Uint8Array(32).fill(1)), y: b64url(new Uint8Array(32).fill(1)) };
    const forged: DeviceRevocation = {
      device_id: me.id,
      revoked_at: null,
      revoked_by: 'device',
      proof: {
        namespace: NAMESPACE,
        device_id: await deviceIdOf(offCurve),
        public_key: offCurve,
        iat: 1_800_000_000,
        id: 'proof-id-0123456789',
        sig: b64url(new Uint8Array(64).fill(2)),
      },
      evidence: null,
    };
    const opts = { deviceId: me.id, namespace: NAMESPACE, trustedDeviceIds: [forged.proof!.device_id] };
    const offCurveVerdict = await verifyDeviceRevocation(forged, opts);
    expect(offCurveVerdict.status).toBe('invalid');
    expect(offCurveVerdict.status === 'invalid' && offCurveVerdict.reason).toMatch(/could not be checked/);

    const revoker = await device('Ed25519');
    const unsupported = await verifyDeviceRevocation(await revokedBy(revoker, me.id), {
      deviceId: me.id,
      namespace: NAMESPACE,
      trustedDeviceIds: [revoker.id],
      verifySignature: async () => {
        throw new Error('Ed25519 is not supported');
      },
    });
    expect(unsupported).toMatchObject({ status: 'invalid', reason: expect.stringContaining('Ed25519 is not supported') });
  });
});

describe('deviceRevocationOf and revokeDevice', () => {
  it('reads the revocation off a DEVICE_REVOKED refusal and nothing else', async () => {
    const revocation = { device_id: 'd', revoked_at: null, revoked_by: 'operator', proof: null, evidence: null };
    const http = new HttpClient({
      baseURL: 'https://gw.example',
      maxRetries: 0,
      fetch: vi.fn(async () =>
        new Response(JSON.stringify({ error: 'revoked', code: 'DEVICE_REVOKED', hint: 'h', revocation }), {
          status: 403,
          headers: { 'content-type': 'application/json' },
        })
      ) as any,
    });
    const err = await http.post('/v1/auth/verify', {}).catch((e) => e);
    expect(deviceRevocationOf(err)).toEqual(revocation);
    expect(deviceRevocationOf(new Error('x'))).toBeUndefined();
    expect(deviceRevocationOf({ code: 'DEVICE_PENDING', details: { revocation } })).toBeUndefined();
  });

  it('sends the evidence beside the device proof', async () => {
    const bodies: any[] = [];
    const fetchImpl = vi.fn(async (_url: any, init: any) => {
      bodies.push(JSON.parse(init.body));
      return new Response(JSON.stringify({ status: 'revoked' }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      });
    });
    const storage = new MemoryStorage();
    const me = await device('Ed25519');
    await storage.set('deviceId', me.id);
    await storage.set('namespace', NAMESPACE);
    const auth = new AuthClient({
      httpClient: new HttpClient({ baseURL: 'https://gw.example', maxRetries: 0, fetch: fetchImpl as any }),
      storage,
    });
    auth.setDeviceSigner(me.signer);
    await auth.revokeDevice('lost-device', { evidence: 'roster-link' });
    expect(bodies[0].evidence).toBe('roster-link');
    expect(bodies[0].device_proof.sig).toBeTruthy();
  });
});
