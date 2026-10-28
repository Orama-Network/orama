import { describe, expect, it, vi } from 'vitest';
import { HttpClient } from '../../../src/core/http';

/**
 * One credential, one header, on every route.
 *
 * There used to be a path-substring switch: database, pubsub and cache calls
 * sent `X-API-Key`, auth calls sent both, everything else sent both the other
 * way round — three spellings chosen by looking at the URL, and the raw key on
 * every request. These are the rules that replaced it.
 */

const KEY = 'orama_rk_3kFj9sPqR2vX7mNb_1a2b3c';
const JWT = 'eyJhbGciOi.wallet.jwt';
const EXCHANGED = 'header.exchanged.signature';

/** A client whose transport records what it was sent. */
function client(options: { apiKey?: string; jwt?: string } = {}) {
  const seen: { headers: Record<string, string>; urls: string[] } = { headers: {}, urls: [] };
  const fetchImpl = vi.fn(async (url: any, init: any) => {
    seen.urls.push(String(url));
    if (String(url).endsWith('/v1/auth/token')) {
      return new Response(JSON.stringify({ access_token: EXCHANGED, expires_in: 900 }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      });
    }
    seen.headers = init?.headers ?? {};
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  });

  const http = new HttpClient({
    baseURL: 'https://gw.example',
    maxRetries: 0,
    fetch: fetchImpl as any,
  });
  if (options.apiKey) {
    http.setApiKey(options.apiKey);
    // The auth client installs this; here it is inline, because what is being
    // tested is what the transport sends once a key becomes a token.
    http.setKeyExchanger(async () => {
      const res = await http.request<{ access_token: string; expires_in: number }>(
        'POST',
        '/v1/auth/token',
        { ownCredential: true, headers: { Authorization: `Bearer ${options.apiKey}` } }
      );
      return { token: res.access_token, expiresAt: Date.now() + res.expires_in * 1000 };
    });
  }
  if (options.jwt) {
    http.setJwt(options.jwt);
  }
  return { http, seen, fetchImpl };
}

const routes = [
  '/v1/rqlite/query',
  '/v1/pubsub/publish',
  '/v1/cache/get',
  '/v1/storage/upload',
  '/v1/proxy/anon',
  '/v1/push/devices',
  '/v1/webrtc/signal',
  '/v1/auth/whoami',
];

describe('every route carries the same one credential', () => {
  it.each(routes)('%s sends only Authorization: Bearer', async (route) => {
    const { http, seen } = client({ apiKey: KEY });
    await http.request('POST', route, {});

    expect(seen.headers['Authorization']).toBe(`Bearer ${EXCHANGED}`);
    expect(seen.headers['X-API-Key']).toBeUndefined();
  });

  it.each(routes)('%s never carries the key itself', async (route) => {
    const { http, seen } = client({ apiKey: KEY });
    await http.request('POST', route, {});

    for (const value of Object.values(seen.headers)) {
      expect(value).not.toContain(KEY);
    }
  });
});

describe('which credential is sent', () => {
  it('a user JWT wins over the key, and nothing else is sent alongside it', async () => {
    const { http, seen } = client({ apiKey: KEY, jwt: JWT });
    await http.request('GET', '/v1/storage/pin', {});

    expect(seen.headers['Authorization']).toBe(`Bearer ${JWT}`);
    expect(seen.headers['X-API-Key']).toBeUndefined();
  });

  it('a client with no credential sends no Authorization header at all', async () => {
    const { http, seen } = client();
    await http.request('GET', '/v1/health', {});

    expect(seen.headers['Authorization']).toBeUndefined();
  });
});

describe('the exchange', () => {
  it('happens once, not once per request', async () => {
    const { http, seen } = client({ apiKey: KEY });

    await http.request('GET', '/v1/cache/get', {});
    await http.request('GET', '/v1/cache/get', {});
    await http.request('GET', '/v1/cache/get', {});

    const exchanges = seen.urls.filter((u) => u.endsWith('/v1/auth/token'));
    expect(exchanges).toHaveLength(1);
  });

  it('carries the key in its own request and nowhere else', async () => {
    const { http, fetchImpl } = client({ apiKey: KEY });
    await http.request('GET', '/v1/cache/get', {});

    const calls = fetchImpl.mock.calls as any[];
    const exchange = calls.find(([url]) => String(url).endsWith('/v1/auth/token'));
    const other = calls.filter(([url]) => !String(url).endsWith('/v1/auth/token'));

    expect(exchange?.[1]?.headers?.['Authorization']).toBe(`Bearer ${KEY}`);
    for (const [, init] of other) {
      expect(JSON.stringify(init?.headers ?? {})).not.toContain(KEY);
    }
  });

  it('is not attempted for a client that was given a token rather than a key', async () => {
    const { http, seen } = client({ jwt: JWT });
    await http.request('GET', '/v1/cache/get', {});

    expect(seen.urls.filter((u) => u.endsWith('/v1/auth/token'))).toHaveLength(0);
  });
});

/**
 * feat-976 review: a revoked device still holds its old token, and the client
 * sent it to the routes it signs in again with. The gateway refuses a revoked
 * device's token on every route, so the sign-in was answered DEVICE_REVOKED and
 * the device could never get a new session.
 */
describe('a route that establishes a session', () => {
  const sessionRoutes = [
    '/v1/auth/challenge',
    '/v1/auth/verify',
    '/v1/auth/refresh',
    '/v1/auth/device',
    '/v1/auth/device/token',
    '/v1/auth/api-key',
  ];

  it.each(sessionRoutes)('%s is sent no stored token', async (route) => {
    const { http, seen } = client({ jwt: JWT });
    await http.request('POST', route, {});

    expect(seen.headers['Authorization']).toBeUndefined();
  });

  it.each(sessionRoutes)('%s does not exchange the key first', async (route) => {
    const { http, seen } = client({ apiKey: KEY });
    await http.request('POST', route, {});

    expect(seen.urls.filter((u) => u.endsWith('/v1/auth/token'))).toHaveLength(0);
    expect(seen.headers['Authorization']).toBeUndefined();
  });

  it('a route that uses the session still carries it', async () => {
    const { http, seen } = client({ jwt: JWT });
    await http.request('POST', '/v1/auth/devices/approve', {});
    await http.request('POST', '/v1/auth/logout', {});

    expect(seen.headers['Authorization']).toBe(`Bearer ${JWT}`);
  });
});
