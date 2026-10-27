import { HttpClient } from "../core/http";
import { Logger } from "../core/logger";
import { AuthCode, AuthError, LogoutError, SDKError } from "../errors";
import { WhoAmI, StorageAdapter, MemoryStorage } from "./types";
import {
  DeviceInfo,
  DeviceLink,
  DeviceProof,
  DeviceProofAction,
  DeviceSigner,
  PendingDeviceApproval,
  makeDeviceProof,
  normalizeUserCode,
} from "./device";

/** Where the id of the device the stored session is bound to is kept. */
const DEVICE_ID_KEY = "deviceId";

/** The tokens a sign-in, a refresh or a collected device link answer with. */
interface SessionResponse {
  access_token: string;
  refresh_token?: string;
  subject: string;
  namespace: string;
  api_key?: string;
  device_id?: string;
  expires_in?: number;
  token_type?: string;
}

/** What to clear, and whether to tell the gateway. */
export interface LogoutOptions {
  /**
   * Keep the API key and make it the active credential again. Use this when
   * the key is an application-level credential and only the signed-in user is
   * leaving. Default: false.
   */
  keepApiKey?: boolean;
  /**
   * Ask the gateway to invalidate the session before clearing locally.
   * Default: true. Set it to false to reset local state only.
   */
  server?: boolean;
}

export class AuthClient {
  private httpClient: HttpClient;
  private storage: StorageAdapter;
  private currentApiKey?: string;
  private currentJwt?: string;
  private deviceSigner?: DeviceSigner;
  private readonly log: Logger;

  constructor(config: {
    httpClient: HttpClient;
    storage?: StorageAdapter;
    apiKey?: string;
    jwt?: string;
  }) {
    this.httpClient = config.httpClient;
    this.storage = config.storage ?? new MemoryStorage();
    this.currentApiKey = config.apiKey;
    this.currentJwt = config.jwt;
    this.log = config.httpClient.logger("Auth");

    if (this.currentApiKey) {
      this.httpClient.setApiKey(this.currentApiKey);
    }
    if (this.currentJwt) {
      this.httpClient.setJwt(this.currentJwt);
    }

    // How a key becomes a token. The key is exchanged once, before the first
    // request, and the token is what every request carries — so the key itself
    // never reaches an access log, a proxy trace or a devtools tab.
    this.httpClient.setKeyExchanger(async (apiKey) => this.exchangeKey(apiKey));

    // A 401 now renews the session and replays the request once. `refresh()`
    // has existed since the first release with nothing calling it, so every
    // application built its own refresh-and-retry loop around every call.
    this.httpClient.setTokenRefresher(async () => {
      if (await this.storage.get("refreshToken")) {
        return this.refresh();
      }
      // No user session. If this client was given a key, the way back is to
      // exchange it again — the token it was carrying has expired, and the key
      // has not.
      if (this.currentApiKey) {
        this.httpClient.clearExchangedToken();
        const { token } = await this.exchangeKey(this.currentApiKey);
        return token;
      }
      return null;
    });
  }

  /**
   * Use a device key. A session bound to the device is refreshed with its
   * proof, and approving a device link or collecting one signs with it. The
   * key stays with the platform; the SDK only asks it to sign.
   */
  setDeviceSigner(signer: DeviceSigner | undefined): void {
    this.deviceSigner = signer;
  }

  /**
   * Exchange an API key for a short-lived token.
   *
   * The gateway answers with the key's own grants embedded, so the token
   * reaches exactly what the key reached and nothing more. It is a plain
   * request rather than one through the client's own pipeline, because the
   * pipeline is what calls this.
   */
  private async exchangeKey(apiKey: string): Promise<{ token: string; expiresAt: number }> {
    const res = await this.httpClient.request<{
      access_token: string;
      expires_in: number;
    }>("POST", "/v1/auth/token", {
      ownCredential: true,
      headers: { Authorization: `Bearer ${apiKey}` },
    });

    if (!res?.access_token) {
      throw new SDKError("the gateway exchanged the key for no token", 500, "AUTH_EXCHANGE_FAILED");
    }
    return {
      token: res.access_token,
      expiresAt: Date.now() + (res.expires_in ?? 900) * 1000,
    };
  }

  setApiKey(apiKey: string) {
    this.currentApiKey = apiKey;
    // Don't clear JWT - it will be cleared explicitly on logout
    this.httpClient.setApiKey(apiKey);
    this.storage.set("apiKey", apiKey);
  }

  setJwt(jwt: string) {
    this.currentJwt = jwt;
    // Don't clear API key - keep it as fallback for after logout
    this.httpClient.setJwt(jwt);
    this.storage.set("jwt", jwt);
  }

  /**
   * Renew a workload's token with the token it is holding.
   *
   * Only a deployment's own token renews this way. A user session is renewed by
   * its refresh token, which rotates and can be revoked; letting any access
   * token mint its own successor would make a stolen one good for ever.
   */
  async renew(): Promise<{ access_token: string; expires_in: number }> {
    return this.httpClient.request("POST", "/v1/auth/renew", {});
  }

  getToken(): string | undefined {
    return this.httpClient.getToken();
  }

  /**
   * Ask the gateway who this client is authenticated as.
   *
   * A rejected credential is an answer: `{ authenticated: false }`. Anything
   * else — an unreachable gateway, a 500, a timeout — is a failure and is
   * thrown. This used to catch everything and report "not authenticated", so an
   * application could not tell a bad API key from a gateway that was down, and
   * a connectivity problem looked like a login problem.
   */
  async whoami(): Promise<WhoAmI> {
    try {
      return await this.httpClient.get<WhoAmI>("/v1/auth/whoami");
    } catch (error) {
      if (error instanceof AuthError || (error instanceof SDKError && error.httpStatus === 401)) {
        return { authenticated: false };
      }
      throw error;
    }
  }

  /**
   * Exchange a stored refresh token for a fresh access token.
   *
   * Pulls the refresh token (and the namespace it was issued for) out of
   * storage — both are persisted by `verify()` after a successful wallet
   * sign-in. The gateway returns a new access token and may rotate the
   * refresh token; we persist the rotated one if present.
   *
   * Bug #239: previously this method (a) sent no body and (b) read the
   * wrong response field, so the call always 400-ed AND silently wrote
   * `undefined` as the in-memory JWT. Both issues fixed.
   */
  async refresh(): Promise<string> {
    const refreshToken = await this.storage.get("refreshToken");
    if (!refreshToken) {
      throw new Error(
        "refresh failed: no refresh token in storage — call verify() first"
      );
    }
    const namespace = (await this.storage.get("namespace")) ?? "default";

    interface RefreshResponse {
      access_token: string;
      refresh_token?: string;
      expires_in?: number;
      subject?: string;
      namespace?: string;
      token_type?: string;
    }
    const body = { refresh_token: refreshToken, namespace };
    // A session bound to a device is refreshed by the device: the gateway
    // refuses the refresh token alone.
    const response = (await this.storage.get(DEVICE_ID_KEY))
      ? await this.sendWithProof("refresh", namespace, refreshToken, (device_proof) =>
          this.httpClient.post<RefreshResponse>("/v1/auth/refresh", { ...body, device_proof })
        )
      : await this.httpClient.post<RefreshResponse>("/v1/auth/refresh", body);

    if (!response?.access_token) {
      throw new Error("refresh failed: server returned no access_token");
    }

    this.setJwt(response.access_token);

    // Rotate the stored refresh token if the server returned a new one
    // (rqlite-side gateway currently echoes the same token; future versions
    // may rotate, so handle both shapes).
    if (response.refresh_token && response.refresh_token !== refreshToken) {
      await this.storage.set("refreshToken", response.refresh_token);
    }

    return response.access_token;
  }

  /**
   * End the session.
   *
   * There used to be three methods for this — `logout`, `logoutUser` and
   * `clear` — differing in two independent ways that their names did not
   * convey: whether the gateway is told, and whether the API key survives.
   * Both are options now.
   *
   * @param options.keepApiKey Keep the API key and make it the active
   * credential again. For a signed-in user leaving an application whose key is
   * application-level.
   * @param options.server Ask the gateway to invalidate the session first.
   *
   * The local session is cleared whether or not the gateway answered. When
   * the gateway could not be told, the promise then rejects with a
   * `LogoutError`: this client is signed out, but the session's tokens stay
   * valid on the gateway until they expire.
   */
  async logout(options: LogoutOptions = {}): Promise<void> {
    const { keepApiKey = false, server = true } = options;

    // Only a JWT has a server-side session; an API key has nothing to end.
    let serverError: SDKError | undefined;
    if (server && this.currentJwt) {
      const body = await this.logoutBody();
      try {
        await this.httpClient.post("/v1/auth/logout", body);
      } catch (error) {
        // HttpClient raises nothing but SDKError.
        serverError = error as SDKError;
      }
    }

    await this.clearLocalSession(keepApiKey);

    if (serverError) {
      throw new LogoutError(serverError);
    }
  }

  /** Forget the session on this device, keeping the API key if asked. */
  private async clearLocalSession(keepApiKey: boolean): Promise<void> {
    this.currentJwt = undefined;
    this.httpClient.setJwt(undefined);

    if (!keepApiKey) {
      this.currentApiKey = undefined;
      this.httpClient.setApiKey(undefined);
      await this.storage.clear();
      return;
    }

    await this.storage.set("jwt", "");

    // The key may only exist in storage, if this client was built from a
    // stored session rather than from a config.
    if (!this.currentApiKey) {
      const stored = await this.storage.get("apiKey");
      if (stored) {
        this.currentApiKey = stored;
      }
    }

    if (this.currentApiKey) {
      this.httpClient.setApiKey(this.currentApiKey);
      this.log.log("API key restored after user logout");
    } else {
      this.log.warn("No API key available after logout");
    }
  }

  /**
   * @deprecated Use `logout({ keepApiKey: true })`.
   */
  async logoutUser(): Promise<void> {
    return this.logout({ keepApiKey: true });
  }

  /**
   * Reset local authentication state without telling the gateway.
   *
   * @deprecated Use `logout({ server: false })`.
   */
  async clear(): Promise<void> {
    return this.logout({ server: false });
  }

  /**
   * Ask the gateway for the message this wallet should sign.
   *
   * `message` is a Sign-In with Ethereum (EIP-4361) message, or its Solana
   * counterpart for `chain_type: "SOL"`. Sign it **verbatim** — it is what the
   * wallet shows the user, and the gateway verifies the signature against the
   * exact text it issued, so re-rendering the same fields produces a signature
   * over different bytes and a failed login.
   *
   * The domain, namespace, nonce and expiry are all inside it. That is the
   * point: a signature over a bare nonce says only that someone holding the key
   * signed some bytes, which makes it valid for any site that ever sees it.
   */
  async challenge(params: {
    wallet: string;
    purpose?: string;
    namespace?: string;
    chain_type?: "ETH" | "SOL";
    /** The device the sign-in will bind: the RFC 7638 thumbprint of its key. */
    device_id?: string;
  }): Promise<{
    message: string;
    nonce: string;
    wallet: string;
    namespace: string;
    chain_type: string;
    issued_at: string;
    expires_at: string;
  }> {
    const response = await this.httpClient.post("/v1/auth/challenge", {
      wallet: params.wallet,
      purpose: params.purpose || "authentication",
      namespace: params.namespace || "default",
      chain_type: params.chain_type || "ETH",
      ...(params.device_id ? { device_id: params.device_id } : {}),
    });
    return response;
  }

  /**
   * Verify a signed sign-in message and get a JWT.
   *
   * Send back the `message` from {@link challenge} exactly as received, with
   * the signature over it. The wallet, the nonce and the namespace are read out
   * of the message rather than passed beside it — those are the fields the user
   * approved, and the ones the signature covers.
   */
  async verify(params: { message: string; signature: string }): Promise<SessionResponse>;
  async verify(params: {
    message: string;
    signature: string;
    device_key: Record<string, string>;
    device_signature: string;
    device_label?: string;
  }): Promise<SessionResponse | PendingDeviceApproval>;
  async verify(params: {
    message: string;
    signature: string;
    /**
     * To bind the session to a device: the device's public JWK, and its
     * signature over the same message (base64url). The challenge must have
     * named the device (`device_id`).
     */
    device_key?: Record<string, string>;
    device_signature?: string;
    device_label?: string;
  }): Promise<SessionResponse | PendingDeviceApproval> {
    const response = await this.httpClient.post("/v1/auth/verify", {
      message: params.message,
      signature: params.signature,
      ...(params.device_key
        ? {
            device_key: params.device_key,
            device_signature: params.device_signature,
            device_label: params.device_label,
          }
        : {}),
    });
    if ((response as PendingDeviceApproval).status === "pending_approval") {
      // No session: the device waits for another of the account's devices.
      return response as PendingDeviceApproval;
    }
    await this.storeSession(response as SessionResponse);
    return response as SessionResponse;
  }

  /** Keep a session the gateway just issued. */
  private async storeSession(response: SessionResponse): Promise<void> {
    // Persist JWT
    this.setJwt(response.access_token);

    // Persist API key if server provided it (created in verifyHandler)
    if (response.api_key) {
      this.setApiKey(response.api_key);
    }

    // Persist refresh token if present (optional, for silent renewal)
    if (response.refresh_token) {
      await this.storage.set("refreshToken", response.refresh_token);
    }

    // Persist the namespace this JWT was issued for so refresh() can
    // include it in the refresh request body (the gateway scopes refresh
    // tokens to the issuing namespace). Bug #239 — without this, refresh
    // would default to "default" and fail for namespace-scoped sessions.
    const issuedNamespace = response.namespace || "default";
    await this.storage.set("namespace", issuedNamespace);

    // A device-bound session is refreshed with the device's proof, so which
    // device it is bound to is kept beside it — and a session bound to none
    // clears what an earlier one left.
    await this.storage.set(DEVICE_ID_KEY, response.device_id ?? "");
  }

  /**
   * What logging out ends. A session bound to a device ends that device's
   * session and nothing else — logging out on one phone must not sign the
   * account out of the others. A session bound to none ends all of them, as
   * it always has.
   */
  private async logoutBody(): Promise<Record<string, unknown>> {
    const refreshToken = await this.storage.get("refreshToken");
    if ((await this.storage.get(DEVICE_ID_KEY)) && refreshToken) {
      const namespace = (await this.storage.get("namespace")) ?? "default";
      return { refresh_token: refreshToken, namespace };
    }
    return { all: true };
  }

  /** The calling account's devices, revoked ones included. */
  async listDevices(): Promise<DeviceInfo[]> {
    const response = await this.httpClient.get<{ devices: DeviceInfo[] }>("/v1/auth/devices");
    return response.devices ?? [];
  }

  /**
   * Revoke one of the account's devices: its sessions, access tokens and open
   * sockets end, and its key can never sign in again. The account's other
   * devices stay signed in.
   */
  async revokeDevice(deviceId: string): Promise<void> {
    // From a device-bound session the device signs the revocation: a lifted
    // access token alone must not sign the account out of its other devices.
    const path = `/v1/auth/devices/${encodeURIComponent(deviceId)}`;
    if (!(await this.storage.get(DEVICE_ID_KEY))) {
      await this.httpClient.request("DELETE", path, { body: {} });
      return;
    }
    const namespace = (await this.storage.get("namespace")) ?? "default";
    await this.sendWithProof("revoke", namespace, deviceId, (device_proof) =>
      this.httpClient.request("DELETE", path, { body: { device_proof } })
    );
  }

  /**
   * Approve a pending device link from this device. The session must be bound
   * to an active device, and the device signer signs the approval.
   */
  async approveDeviceLink(userCode: string): Promise<void> {
    const namespace = (await this.storage.get("namespace")) ?? "default";
    const code = normalizeUserCode(userCode);
    await this.sendWithProof("approve", namespace, code, (device_proof) =>
      this.httpClient.post("/v1/auth/devices/approve", { user_code: code, device_proof })
    );
  }

  /**
   * Start linking this device to an account without a wallet: the gateway
   * holds a pending login for this device's key, and one of the account's
   * devices approves `user_code` with `approveDeviceLink`.
   */
  async startDeviceLink(params: { namespace: string; label?: string }): Promise<DeviceLink> {
    return this.httpClient.post<DeviceLink>("/v1/auth/device", {
      namespace: params.namespace,
      device_key: this.requireDeviceSigner("link").publicJwk,
      ...(params.label ? { device_label: params.label } : {}),
    });
  }

  /**
   * Collect the session of an approved device link — started with
   * `startDeviceLink`, or by a sign-in that answered `pending_approval`. The
   * device signer proves this is the device the link was started for. While
   * nobody has approved it yet the gateway answers `authorization_pending`.
   */
  async claimDeviceLink(deviceCode: string, namespace: string): Promise<SessionResponse> {
    const response = await this.sendWithProof("claim", namespace, deviceCode, (device_proof) =>
      this.httpClient.post<SessionResponse>("/v1/auth/device/token", {
        device_code: deviceCode,
        device_proof,
      })
    );
    await this.storeSession(response);
    return response;
  }

  /**
   * Send a request carrying a device proof, stamped on the gateway's clock.
   *
   * The proof's time is the gateway's as this client last saw it (every
   * answer's `Date` header), so a phone whose own clock is minutes off still
   * proves inside the gateway's window. When nothing has been heard from the
   * gateway yet, the first proof goes on the device's clock; if the gateway
   * refuses it for its time it answers with its own (`server_time`), and the
   * proof is made again on that clock and sent once more. That is the
   * refusal's purpose, as DPoP's `use_dpop_nonce` hands a client the nonce to
   * use: a fresh proof with a new id, never the refused one replayed.
   */
  private async sendWithProof<T>(
    action: DeviceProofAction,
    namespace: string,
    binding: string,
    send: (proof: DeviceProof) => Promise<T>
  ): Promise<T> {
    const signer = this.requireDeviceSigner(action);
    const prove = () => makeDeviceProof(signer, action, namespace, binding, this.httpClient.serverNow());
    try {
      return await send(await prove());
    } catch (error) {
      const serverTime =
        error instanceof SDKError && error.code === AuthCode.DeviceProofClockSkew
          ? error.details?.server_time
          : undefined;
      if (typeof serverTime !== "number") {
        throw error;
      }
      this.httpClient.observeServerTime(serverTime * 1000);
      return send(await prove());
    }
  }

  private requireDeviceSigner(what: string): DeviceSigner {
    if (!this.deviceSigner) {
      throw new SDKError(
        `${what} needs the device key: call auth.setDeviceSigner() with the platform's key first`,
        0,
        "DEVICE_PROOF_REQUIRED"
      );
    }
    return this.deviceSigner;
  }

  /**
   * Get an API key for a namespace the wallet is already a member of. Takes
   * the same signed message as {@link verify}.
   *
   * It does not create ownership — it used to, which is how naming a namespace
   * you did not own made you its owner. Ownership comes from creating the
   * namespace, and the lobby namespace has no keys at all.
   */
  async getApiKey(params: {
    message: string;
    signature: string;
    plan?: string;
  }): Promise<{
    api_key: string;
    namespace: string;
    wallet: string;
  }> {
    const response = await this.httpClient.post("/v1/auth/api-key", {
      message: params.message,
      signature: params.signature,
      ...(params.plan ? { plan: params.plan } : {}),
    });

    // Automatically set the API key
    this.setApiKey(response.api_key);

    return response;
  }
}
