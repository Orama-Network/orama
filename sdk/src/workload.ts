import { ClientConfig, Client, createClient } from "./index";
import { SDKError } from "./errors";

/**
 * A client for code running as a deployment.
 *
 * A deployed app used to carry a key somebody had pasted into it — a namespace
 * key, so an application compromise was a namespace takeover. It is handed a
 * short-lived token of its own now, in the file named by `$ORAMA_TOKEN_FILE`,
 * and it renews that token with the gateway before it expires.
 *
 * ```ts
 * import { createWorkloadClient } from "@orama-network/orama/workload";
 * const client = await createWorkloadClient();
 * ```
 *
 * It is its own entry, not part of the default one: its default token reader
 * imports `node:fs/promises`, and Metro (React Native) resolves dynamic
 * imports too, so it would fail every app's bundle.
 *
 * There is nothing to configure: the gateway's URL, the namespace and the token
 * all come from the environment the platform set up.
 */

/** How long before a workload token expires it is renewed. */
const RENEWAL_MARGIN_MS = 5 * 60_000;

export interface WorkloadClientConfig extends Omit<ClientConfig, "baseURL" | "apiKey" | "jwt"> {
  /** The gateway to talk to. Defaults to `$ORAMA_GATEWAY_URL`. */
  baseURL?: string;
  /** Where the token is. Defaults to `$ORAMA_TOKEN_FILE`. */
  tokenFile?: string;
  /**
   * How the token file is read. Defaults to Node's `fs/promises`; supply one to
   * test without a filesystem.
   */
  readFile?: (path: string) => Promise<string>;
}

/**
 * Build a client from the environment a deployment runs in, and keep its token
 * fresh.
 *
 * The returned client renews on a timer at 55 minutes of an hour, and again
 * whenever the gateway says the token has expired. Nothing long-lived is ever
 * held.
 */
export async function createWorkloadClient(
  config: WorkloadClientConfig = {}
): Promise<Client> {
  const baseURL = config.baseURL ?? readEnv("ORAMA_GATEWAY_URL");
  if (!baseURL) {
    throw new SDKError(
      "no gateway to talk to: ORAMA_GATEWAY_URL is unset, so this is not running as an Orama deployment",
      500,
      "WORKLOAD_NO_GATEWAY"
    );
  }

  const tokenFile = config.tokenFile ?? readEnv("ORAMA_TOKEN_FILE");
  if (!tokenFile) {
    throw new SDKError(
      "no credential: ORAMA_TOKEN_FILE is unset. A deployment is handed one by the platform; " +
        "if this is running outside one, use createClient with a key instead",
      500,
      "WORKLOAD_NO_TOKEN"
    );
  }

  const read = config.readFile ?? defaultReadFile;
  const token = (await read(tokenFile)).trim();
  if (!token) {
    throw new SDKError(
      `the credential at ${tokenFile} is empty`,
      500,
      "WORKLOAD_EMPTY_TOKEN"
    );
  }

  const client = createClient({ ...config, baseURL, jwt: token });

  // Renew with the token we hold. The gateway resolves the deployment's grants
  // at mint time, so a grant taken away reaches this process on its next
  // renewal rather than only on its next deploy.
  const renew = async (): Promise<void> => {
    const res = await client.auth.renew();
    client.auth.setJwt(res.access_token);
    schedule(res.expires_in * 1000);
  };

  let timer: ReturnType<typeof setTimeout> | undefined;
  const schedule = (lifetimeMs: number) => {
    if (timer) {
      clearTimeout(timer);
    }
    const delay = Math.max(lifetimeMs - RENEWAL_MARGIN_MS, 30_000);
    timer = setTimeout(() => {
      void renew().catch(() => {
        // The next attempt is soon, and the token in hand is still valid until
        // its own expiry. Nothing here should take a running app down.
        schedule(RENEWAL_MARGIN_MS);
      });
    }, delay);
    // Do not hold the process open for a renewal.
    (timer as unknown as { unref?: () => void }).unref?.();
  };

  schedule(60 * 60_000);
  return client;
}

function readEnv(name: string): string | undefined {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  return env?.[name];
}

async function defaultReadFile(path: string): Promise<string> {
  const { readFile } = await import("node:fs/promises");
  return readFile(path, "utf8");
}
