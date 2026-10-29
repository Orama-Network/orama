import { describe, expect, it, vi } from 'vitest';
import { WSClient, WSCloseCode } from '../../../src/core/ws';

/**
 * There was no reconnection. A dropped socket stayed dropped and the
 * subscription went quiet for the life of the process, while the README
 * advertised "automatic reconnection" — so every application wrote its own
 * loop on top of `onClose`.
 */

type Listener = (event: any) => void;

/** A WebSocket stand-in a test can open, drop and inspect. */
class FakeSocket {
  static instances: FakeSocket[] = [];
  static OPEN = 1;

  readyState = 0;
  private listeners: Record<string, Listener[]> = {};

  constructor(public url: string) {
    FakeSocket.instances.push(this);
  }

  addEventListener(type: string, fn: Listener) {
    (this.listeners[type] ??= []).push(fn);
  }

  close() {
    this.emit('close', { code: 1000, reason: 'closed by client' });
  }

  send() {
    /* not exercised here */
  }

  /** Complete the handshake. */
  open() {
    this.readyState = FakeSocket.OPEN;
    this.emit('open', {});
  }

  /** Drop the connection the way a network failure does. */
  drop(code = 1006) {
    this.readyState = 3;
    this.emit('close', { code, reason: '' });
  }

  message(data: string) {
    this.emit('message', { data });
  }

  private emit(type: string, event: any) {
    (this.listeners[type] ?? []).forEach((fn) => fn({ type, ...event }));
  }
}

function client(overrides: Record<string, unknown> = {}) {
  FakeSocket.instances = [];
  return new WSClient({
    wsURL: 'wss://gw.example/v1/pubsub/ws?topic=room',
    WebSocket: FakeSocket as any,
    timeout: 1000,
    reconnect: { initialDelayMs: 1, maxDelayMs: 2, ...(overrides.reconnect as object) },
    ...overrides,
  });
}

/** Open the client against the socket it just created. */
async function connected(ws: WSClient) {
  const connecting = ws.connect();
  FakeSocket.instances[0].open();
  await connecting;
  return FakeSocket.instances[0];
}

/** Let pending timers and microtasks run. */
async function settle(ms = 20) {
  await new Promise((resolve) => setTimeout(resolve, ms));
}

describe('a dropped connection is re-established', () => {
  it('opens a new socket after an unexpected close', async () => {
    const ws = client();
    const first = await connected(ws);

    first.drop();
    await settle();

    expect(FakeSocket.instances).toHaveLength(2);
    expect(FakeSocket.instances[1].url).toBe(first.url);
  });

  it('resumes delivering messages on the new socket', async () => {
    const ws = client();
    const received: string[] = [];
    ws.onMessage((data) => received.push(data));

    const first = await connected(ws);
    first.message('before');

    first.drop();
    await settle();
    FakeSocket.instances[1].open();
    FakeSocket.instances[1].message('after');

    expect(received).toEqual(['before', 'after']);
  });

  it('announces the attempt and the recovery', async () => {
    const ws = client();
    const attempts: number[] = [];
    const recovered: number[] = [];
    ws.onReconnecting((attempt) => attempts.push(attempt));
    ws.onReconnected((attempt) => recovered.push(attempt));

    const first = await connected(ws);
    first.drop();
    await settle();
    FakeSocket.instances[1].open();
    await settle();

    expect(attempts).toEqual([1]);
    expect(recovered).toEqual([1]);
  });

  /**
   * The close handlers are the application's "this subscription has ended"
   * signal. Firing them for a drop that is about to be repaired would tell an
   * application to tear down a subscription that is coming back.
   */
  it('does not report a recoverable drop as a close', async () => {
    const ws = client();
    const closes: number[] = [];
    ws.onClose((code) => closes.push(code));

    const first = await connected(ws);
    first.drop();
    await settle();

    expect(closes).toEqual([]);
  });

  it('reports a close the caller asked for', async () => {
    const ws = client();
    const closes: number[] = [];
    ws.onClose((code) => closes.push(code));

    const first = await connected(ws);
    expect(first).toBeDefined();
    ws.close();
    await settle();

    expect(closes).toEqual([1000]);
    expect(FakeSocket.instances).toHaveLength(1);
  });

  /**
   * feat-976: the gateway closes a revoked device's socket with 4410. A
   * reconnect with the same token can never succeed, and retrying would hide
   * from the application that the device was revoked.
   */
  it('does not reconnect a revoked device, and reports the close', async () => {
    const ws = client();
    const closes: number[] = [];
    ws.onClose((code) => closes.push(code));

    const first = await connected(ws);
    first.drop(WSCloseCode.DeviceRevoked);
    await settle();

    expect(FakeSocket.instances).toHaveLength(1);
    expect(closes).toEqual([4410]);
  });

  it('stops trying once the caller has closed', async () => {
    const ws = client();
    const first = await connected(ws);

    first.drop();
    ws.close();
    await settle();

    expect(FakeSocket.instances).toHaveLength(1);
  });

  it('gives up after the configured number of attempts, then reports the close', async () => {
    const ws = client({ reconnect: { enabled: true, maxAttempts: 3, initialDelayMs: 1, maxDelayMs: 2 } });
    const closes: number[] = [];
    ws.onClose((code) => closes.push(code));

    const first = await connected(ws);
    first.drop();

    // Each new socket fails the same way.
    for (let i = 0; i < 5; i++) {
      await settle(10);
      const latest = FakeSocket.instances[FakeSocket.instances.length - 1];
      if (latest !== first && latest.readyState !== 3) latest.drop();
    }
    await settle(20);

    // One original plus three attempts.
    expect(FakeSocket.instances).toHaveLength(4);
    expect(closes).toHaveLength(1);
  });

  it('resets the attempt count after a success, so a later drop gets the full budget', async () => {
    const ws = client({ reconnect: { enabled: true, maxAttempts: 2, initialDelayMs: 1, maxDelayMs: 2 } });
    const attempts: number[] = [];
    ws.onReconnecting((attempt) => attempts.push(attempt));

    const first = await connected(ws);
    first.drop();
    await settle();
    FakeSocket.instances[1].open();
    await settle();

    FakeSocket.instances[1].drop();
    await settle();

    expect(attempts).toEqual([1, 1]);
  });

  it('can be turned off', async () => {
    const ws = client({ reconnect: { enabled: false } });
    const closes: number[] = [];
    ws.onClose((code) => closes.push(code));

    const first = await connected(ws);
    first.drop();
    await settle();

    expect(FakeSocket.instances).toHaveLength(1);
    expect(closes).toEqual([1006]);
  });

  /**
   * A connection that never opened is the caller's failure to report, not
   * something to retry behind their back: `connect()` rejects and the caller
   * decides.
   */
  it('does not retry a connection that never opened', async () => {
    const ws = client();
    const connecting = ws.connect();
    FakeSocket.instances[0].drop();

    await expect(connecting).rejects.toBeDefined();
    await settle();

    expect(FakeSocket.instances).toHaveLength(1);
  });
});

describe('backoff', () => {
  it('grows between attempts and stays under the ceiling', async () => {
    const delays: number[] = [];
    const ws = client({
      reconnect: { enabled: true, maxAttempts: 4, initialDelayMs: 100, maxDelayMs: 250 },
    });
    ws.onReconnecting((_attempt, delay) => delays.push(delay));

    const first = await connected(ws);
    const timers: any[] = [];
    vi.spyOn(globalThis, 'setTimeout').mockImplementation(((fn: () => void) => {
      timers.push(fn);
      return 0 as unknown as ReturnType<typeof setTimeout>;
    }) as typeof setTimeout);

    try {
      first.drop();
      // Fire each scheduled attempt, and drop the socket it creates.
      for (let i = 0; i < 3; i++) {
        const run = timers.shift();
        if (!run) break;
        run();
        const latest = FakeSocket.instances[FakeSocket.instances.length - 1];
        latest.drop();
      }
    } finally {
      vi.restoreAllMocks();
    }

    expect(delays.length).toBeGreaterThanOrEqual(3);
    // 100, 200, then capped at 250 — each with up to 25% jitter on top.
    expect(delays[0]).toBeGreaterThanOrEqual(100);
    expect(delays[0]).toBeLessThanOrEqual(125);
    expect(delays[1]).toBeGreaterThanOrEqual(200);
    expect(delays[1]).toBeLessThanOrEqual(250);
    expect(delays[2]).toBeGreaterThanOrEqual(250);
    expect(delays[2]).toBeLessThanOrEqual(313);
  });
});

describe('WSClient auth parameter', () => {
  it('sends the token as ?jwt=, the parameter the gateway verifies as a JWT on an upgrade', () => {
    FakeSocket.instances = [];
    const ws = new WSClient({
      wsURL: 'wss://gw.example/v1/pubsub/ws?topic=t',
      authToken: 'a.b.c',
      WebSocket: FakeSocket as any,
      reconnect: { enabled: false },
    });
    void ws.connect().catch(() => undefined);
    const url = new URL(FakeSocket.instances[0].url);
    expect(url.searchParams.get('jwt')).toBe('a.b.c');
    expect(url.searchParams.get('token')).toBeNull();
    expect(url.searchParams.get('topic')).toBe('t');
  });

  it('sends no credential parameter without a token', () => {
    FakeSocket.instances = [];
    const ws = new WSClient({
      wsURL: 'wss://gw.example/v1/pubsub/ws?topic=t',
      WebSocket: FakeSocket as any,
      reconnect: { enabled: false },
    });
    void ws.connect().catch(() => undefined);
    const url = new URL(FakeSocket.instances[0].url);
    expect(url.searchParams.get('jwt')).toBeNull();
    expect(url.searchParams.get('token')).toBeNull();
  });
});
