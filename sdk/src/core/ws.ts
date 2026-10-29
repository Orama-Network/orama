import WebSocket from "isomorphic-ws";
import { SDKError } from "../errors";
import { NetworkErrorCallback } from "./http";
import { Logger } from "./logger";

export interface WSClientConfig {
  wsURL: string;
  timeout?: number;
  authToken?: string;
  WebSocket?: typeof WebSocket;
  /**
   * Callback invoked on WebSocket errors.
   * Use this to trigger gateway failover at the application layer.
   */
  onNetworkError?: NetworkErrorCallback;
  /**
   * Where to write debug lines. Defaults to a logger that prints nothing:
   * a WebSocket client built directly, rather than through `createClient`,
   * has no `debug` setting to inherit.
   */
  logger?: Logger;
  /** How to recover a connection that drops. See `ReconnectConfig`. */
  reconnect?: Partial<ReconnectConfig>;
}

/**
 * Recovery policy for a connection that closes without being asked to.
 *
 * There was none: a dropped socket stayed dropped and the subscription went
 * quiet for the life of the process, while the README advertised "automatic
 * reconnection". Every application therefore wrote its own loop on top of
 * `onClose`.
 */
export interface ReconnectConfig {
  /** Default: true. */
  enabled: boolean;
  /** Give up after this many consecutive failures. Default: 10. */
  maxAttempts: number;
  /** Delay before the first attempt. Default: 500ms. */
  initialDelayMs: number;
  /** Ceiling for the backoff. Default: 30s. */
  maxDelayMs: number;
}

const DEFAULT_RECONNECT: ReconnectConfig = {
  enabled: true,
  maxAttempts: 10,
  initialDelayMs: 500,
  maxDelayMs: 30_000,
};

/** Fraction of each backoff added as jitter, so clients do not return in step. */
const RECONNECT_JITTER_RATIO = 0.25;

/**
 * Close codes the gateway ends a socket with, mirroring
 * `core/pkg/gateway/wssession/registry.go`.
 */
export const WSCloseCode = {
  /**
   * The device the socket's token is bound to was revoked. Final: the client
   * does not reconnect, since the device can never hold a session again. The
   * close frame cannot carry the revocation; any request with the same token
   * answers `DEVICE_REVOKED` with it (`deviceRevocationOf`), which the
   * application checks (`verifyDeviceRevocation`) before it acts on it.
   */
  DeviceRevoked: 4410,
} as const;

export type WSMessageHandler = (data: string) => void;
export type WSErrorHandler = (error: Error) => void;
export type WSCloseHandler = (code: number, reason: string) => void;
export type WSOpenHandler = () => void;
/** Called before each reconnection attempt, with the 1-based attempt number. */
export type WSReconnectingHandler = (attempt: number, delayMs: number) => void;
/** Called when a reconnection attempt has succeeded. */
export type WSReconnectedHandler = (attempt: number) => void;

/**
 * WebSocket client for one gateway. A connection that closes without being
 * asked to is retried as `ReconnectConfig` says; moving to another gateway is
 * left to the application (`onNetworkError`).
 */
export class WSClient {
  private wsURL: string;
  private timeout: number;
  private authToken?: string;
  private WebSocketClass: typeof WebSocket;
  private onNetworkError?: NetworkErrorCallback;
  private readonly log: Logger;
  private readonly reconnectConfig: ReconnectConfig;

  private ws?: WebSocket;
  private messageHandlers: Set<WSMessageHandler> = new Set();
  private errorHandlers: Set<WSErrorHandler> = new Set();
  private closeHandlers: Set<WSCloseHandler> = new Set();
  private openHandlers: Set<WSOpenHandler> = new Set();
  private reconnectingHandlers: Set<WSReconnectingHandler> = new Set();
  private reconnectedHandlers: Set<WSReconnectedHandler> = new Set();
  private isClosed = false;
  /** Set once the caller has connected, so a drop is known to be unexpected. */
  private wasConnected = false;
  private reconnectAttempt = 0;
  private reconnectTimer?: ReturnType<typeof setTimeout>;

  constructor(config: WSClientConfig) {
    this.wsURL = config.wsURL;
    this.timeout = config.timeout ?? 30000;
    this.authToken = config.authToken;
    this.WebSocketClass = config.WebSocket ?? WebSocket;
    this.onNetworkError = config.onNetworkError;
    this.log = config.logger ?? Logger.disabled();
    this.reconnectConfig = { ...DEFAULT_RECONNECT, ...config.reconnect };
  }

  /**
   * Set the network error callback
   */
  setOnNetworkError(callback: NetworkErrorCallback | undefined): void {
    this.onNetworkError = callback;
  }

  /**
   * Get the current WebSocket URL
   */
  get url(): string {
    return this.wsURL;
  }

  /**
   * Connect to WebSocket server
   */
  connect(): Promise<void> {
    this.isClosed = false;
    return this.open().then(() => {
      this.wasConnected = true;
    });
  }

  /** One connection attempt. */
  private open(): Promise<void> {
    return new Promise((resolve, reject) => {
      // Cleared first so a construction failure is distinguishable from a
      // socket that opened and then closed: only the latter emits `close`.
      this.ws = undefined;
      try {
        const wsUrl = this.buildWSUrl();
        this.ws = new this.WebSocketClass(wsUrl);

        const timeout = setTimeout(() => {
          this.ws?.close();
          const error = new SDKError("WebSocket connection timeout", 408, "WS_TIMEOUT");

          // Call the network error callback if configured
          if (this.onNetworkError) {
            this.onNetworkError(error, {
              method: "WS",
              path: this.wsURL,
              isRetry: false,
              attempt: 0,
            });
          }

          reject(error);
        }, this.timeout);

        let opened = false;

        this.ws.addEventListener("open", () => {
          clearTimeout(timeout);
          opened = true;
          this.log.log(`Connected to ${this.wsURL}`);
          this.openHandlers.forEach((handler) => handler());
          resolve();
        });

        this.ws.addEventListener("message", (event: Event) => {
          const msgEvent = event as MessageEvent;
          this.messageHandlers.forEach((handler) => handler(msgEvent.data));
        });

        this.ws.addEventListener("error", (event: Event) => {
          this.log.error("WebSocket error:", event);
          clearTimeout(timeout);
          // Extract useful details from the event — raw Event objects don't serialize
          const details: Record<string, any> = { type: event.type };
          if ("message" in event) {
            details.message = (event as ErrorEvent).message;
          }
          const error = new SDKError("WebSocket error", 0, "WS_ERROR", details);

          // Call the network error callback if configured
          if (this.onNetworkError) {
            this.onNetworkError(error, {
              method: "WS",
              path: this.wsURL,
              isRetry: false,
              attempt: 0,
            });
          }

          this.errorHandlers.forEach((handler) => handler(error));
          reject(error);
        });

        this.ws.addEventListener("close", (event: Event) => {
          clearTimeout(timeout);
          const closeEvent = event as CloseEvent;
          const code = closeEvent.code ?? 1006;
          const reason = closeEvent.reason ?? "";
          this.log.log(
            `Connection closed (code: ${code}, reason: ${reason || "none"})`
          );

          // A socket that closes before it opened is a failed attempt: settle
          // the promise now rather than leaving the caller waiting out the full
          // connection timeout for a server that has already hung up.
          if (!opened) {
            reject(
              new SDKError(
                `WebSocket closed before it opened (code: ${code})`,
                0,
                "WS_CLOSED",
                { code, reason }
              )
            );
          }

          this.handleClose(code, reason);
        });
      } catch (error) {
        reject(error);
      }
    });
  }

  /**
   * Decide what a closed socket means.
   *
   * A close the caller asked for, one that arrives before the first
   * successful connect, or the gateway's close of a revoked device's socket
   * (`WSCloseCode.DeviceRevoked`) is final. Anything else is a drop, and the
   * connection is re-established with backoff. The close handlers fire only when the
   * connection is finished for good, so an application is not told the
   * subscription has ended while it is being restored.
   */
  private handleClose(code: number, reason: string): void {
    const shouldReconnect =
      this.reconnectConfig.enabled &&
      !this.isClosed &&
      this.wasConnected &&
      code !== WSCloseCode.DeviceRevoked &&
      this.reconnectAttempt < this.reconnectConfig.maxAttempts;

    if (!shouldReconnect) {
      this.closeHandlers.forEach((handler) => handler(code, reason));
      return;
    }

    this.reconnectAttempt += 1;
    const delay = this.reconnectDelay(this.reconnectAttempt);
    this.log.log(
      `Reconnecting in ${delay}ms (attempt ${this.reconnectAttempt}/${this.reconnectConfig.maxAttempts})`
    );
    this.reconnectingHandlers.forEach((handler) =>
      handler(this.reconnectAttempt, delay)
    );

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      if (this.isClosed) return;
      const attempt = this.reconnectAttempt;
      this.open()
        .then(() => {
          this.reconnectAttempt = 0;
          this.log.log(`Reconnected after ${attempt} attempt(s)`);
          this.reconnectedHandlers.forEach((handler) => handler(attempt));
        })
        .catch(() => {
          // A socket that was constructed emits `close` after it fails, and
          // that event drives the next attempt. Only a construction failure
          // leaves no socket and no event, so the sequence is continued from
          // here for that case alone — otherwise every failure would schedule
          // two attempts.
          if (!this.isClosed && !this.ws) {
            this.handleClose(1006, "reconnect failed");
          }
        });
    }, delay);
  }

  /** Exponential backoff with jitter, capped. */
  private reconnectDelay(attempt: number): number {
    const base = Math.min(
      this.reconnectConfig.initialDelayMs * 2 ** (attempt - 1),
      this.reconnectConfig.maxDelayMs
    );
    return Math.round(base + base * RECONNECT_JITTER_RATIO * Math.random());
  }

  /**
   * Build WebSocket URL with auth token
   */
  private buildWSUrl(): string {
    let url = this.wsURL;

    if (this.authToken) {
      // Always `jwt`, always a token. This used to look at the credential
      // and send `api_key=<the key itself>` when it started with `ak_`, so a
      // long-lived key went into the URL — into the gateway's access log, and
      // into the browser's history. The SDK exchanges a key for a token before
      // it opens a socket. The gateway verifies a JWT in `?jwt=` on an upgrade
      // (and strips it before the proxy hop); `?token=` is read as an API key,
      // so a token sent there is looked up as one and refused.
      const separator = url.includes("?") ? "&" : "?";
      url += `${separator}jwt=${encodeURIComponent(this.authToken)}`;
    }

    return url;
  }

  /**
   * Register message handler
   */
  onMessage(handler: WSMessageHandler): () => void {
    this.messageHandlers.add(handler);
    return () => this.messageHandlers.delete(handler);
  }

  /**
   * Unregister message handler
   */
  offMessage(handler: WSMessageHandler): void {
    this.messageHandlers.delete(handler);
  }

  /**
   * Register error handler
   */
  onError(handler: WSErrorHandler): () => void {
    this.errorHandlers.add(handler);
    return () => this.errorHandlers.delete(handler);
  }

  /**
   * Unregister error handler
   */
  offError(handler: WSErrorHandler): void {
    this.errorHandlers.delete(handler);
  }

  /**
   * Register close handler
   */
  onClose(handler: WSCloseHandler): () => void {
    this.closeHandlers.add(handler);
    return () => this.closeHandlers.delete(handler);
  }

  /**
   * Unregister close handler
   */
  offClose(handler: WSCloseHandler): void {
    this.closeHandlers.delete(handler);
  }

  /**
   * Register open handler
   */
  onOpen(handler: WSOpenHandler): () => void {
    this.openHandlers.add(handler);
    return () => this.openHandlers.delete(handler);
  }

  /**
   * Register a handler called before each reconnection attempt.
   */
  onReconnecting(handler: WSReconnectingHandler): () => void {
    this.reconnectingHandlers.add(handler);
    return () => this.reconnectingHandlers.delete(handler);
  }

  /**
   * Register a handler called when the connection has been restored.
   */
  onReconnected(handler: WSReconnectedHandler): () => void {
    this.reconnectedHandlers.add(handler);
    return () => this.reconnectedHandlers.delete(handler);
  }

  /**
   * Send data through WebSocket
   */
  send(data: string): void {
    if (this.ws?.readyState !== WebSocket.OPEN) {
      throw new SDKError("WebSocket is not connected", 0, "WS_NOT_CONNECTED");
    }
    this.ws.send(data);
  }

  /**
   * Close WebSocket connection
   */
  close(): void {
    if (this.isClosed) {
      return;
    }
    this.isClosed = true;
    if (this.reconnectTimer !== undefined) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = undefined;
    }
    this.ws?.close();
  }

  /**
   * Check if WebSocket is connected
   */
  isConnected(): boolean {
    return !this.isClosed && this.ws?.readyState === WebSocket.OPEN;
  }

  /**
   * Update auth token
   */
  setAuthToken(token?: string): void {
    this.authToken = token;
  }
}
