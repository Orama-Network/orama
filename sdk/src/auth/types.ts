export interface AuthConfig {
  apiKey?: string;
  jwt?: string;
}

/**
 * The answer from `GET /v1/auth/whoami`.
 *
 * `method` says which credential the gateway used to identify the caller, and
 * `subject` is the wallet address or API key id behind it. Both were missing
 * from this type while the gateway had been returning them all along.
 */
export interface WhoAmI {
  authenticated: boolean;
  /** Which credential was used: a session token or an API key. */
  method?: "jwt" | "api_key";
  /** Wallet address (JWT) or key subject. */
  subject?: string;
  namespace?: string;
  issuer?: string;
  audience?: string;
  issued_at?: number;
  not_before?: number;
  expires_at?: number;
  /** The device a device-bound session is bound to. */
  device_id?: string;
  /**
   * That device's state: `pending` for the restricted session of a device the
   * namespace has yet to approve, which holds no grant, or `active`.
   */
  device_state?: "pending" | "active";
  /** @deprecated The gateway does not return this; use `subject`. */
  address?: string;
}

export interface StorageAdapter {
  get(key: string): Promise<string | null>;
  set(key: string, value: string): Promise<void>;
  clear(): Promise<void>;
}

export class MemoryStorage implements StorageAdapter {
  private storage: Map<string, string> = new Map();

  async get(key: string): Promise<string | null> {
    return this.storage.get(key) ?? null;
  }

  async set(key: string, value: string): Promise<void> {
    this.storage.set(key, value);
  }

  async clear(): Promise<void> {
    this.storage.clear();
  }
}

export class LocalStorageAdapter implements StorageAdapter {
  private prefix = "@network/sdk:";

  async get(key: string): Promise<string | null> {
    if (typeof globalThis !== "undefined" && globalThis.localStorage) {
      return globalThis.localStorage.getItem(this.prefix + key);
    }
    return null;
  }

  async set(key: string, value: string): Promise<void> {
    if (typeof globalThis !== "undefined" && globalThis.localStorage) {
      globalThis.localStorage.setItem(this.prefix + key, value);
    }
  }

  async clear(): Promise<void> {
    if (typeof globalThis !== "undefined" && globalThis.localStorage) {
      const keysToDelete: string[] = [];
      for (let i = 0; i < globalThis.localStorage.length; i++) {
        const key = globalThis.localStorage.key(i);
        if (key?.startsWith(this.prefix)) {
          keysToDelete.push(key);
        }
      }
      keysToDelete.forEach((key) => globalThis.localStorage.removeItem(key));
    }
  }
}
