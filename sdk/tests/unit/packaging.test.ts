import { describe, expect, it } from 'vitest';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { builtinModules } from 'node:module';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';

/**
 * The package was ESM-only while its README promised isomorphic use, so
 * `require('@orama-network/orama')` failed outright at import: Jest without ESM
 * support, ts-node in CJS mode, and CJS server code in a Next.js app all hit
 * ERR_REQUIRE_ESM. These lock the packaging down.
 *
 * The build tests need `pnpm build` to have run; they skip rather than fail
 * when dist is absent, so `pnpm test` on a clean checkout still passes.
 */

const root = resolve(__dirname, '../..');
const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const distBuilt = existsSync(join(root, 'dist/index.js'));

describe('package.json', () => {
  it('declares both module formats in exports', () => {
    const entry = pkg.exports['.'];
    expect(entry.import).toBeDefined();
    expect(entry.require).toBeDefined();
  });

  it('gives each format its own type declarations', () => {
    // A `require` resolving to the ESM .d.ts confuses TypeScript under
    // moduleResolution: node16, which reads the condition it matched.
    expect(pkg.exports['.'].import.types).toBe('./dist/index.d.ts');
    expect(pkg.exports['.'].require.types).toBe('./dist/index.d.cts');
  });

  it('points main at the CJS build and module at the ESM build', () => {
    // Bundlers and tools that predate the exports map read these.
    expect(pkg.main).toBe('./dist/index.cjs');
    expect(pkg.module).toBe('./dist/index.js');
  });

  it('is marked side-effect free so bundlers can tree-shake it', () => {
    expect(pkg.sideEffects).toBe(false);
  });

  it('points at the repository it actually lives in', () => {
    // It said Orama-Network/orama/core, which is not where this is.
    expect(pkg.repository.url).toContain('Orama-Network/orama');
    expect(pkg.bugs.url).toContain('Orama-Network/orama');
  });

  it('exposes the chain module as its own entry, so other clients do not carry its codecs', () => {
    expect(pkg.exports['./chain'].import.default).toBe('./dist/chain.js');
    expect(pkg.exports['./chain'].require.default).toBe('./dist/chain.cjs');
    expect(pkg.exports['./chain'].import.types).toBe('./dist/chain.d.ts');
    expect(pkg.exports['./chain'].require.types).toBe('./dist/chain.d.cts');
  });

  it('exposes the relayed-fetch transport as its own Node-only entry, so the core never imports node:tls', () => {
    expect(pkg.exports['./relay'].import.default).toBe('./dist/relay.js');
    expect(pkg.exports['./relay'].require.default).toBe('./dist/relay.cjs');
    expect(pkg.exports['./relay'].import.types).toBe('./dist/relay.d.ts');
    expect(pkg.exports['./relay'].require.types).toBe('./dist/relay.d.cts');
    const core = readFileSync(join(root, 'src/index.ts'), 'utf8');
    expect(core).not.toMatch(/relay-transport/);
  });

  it('exposes the workload client as its own entry, so the core never imports node:fs/promises', () => {
    expect(pkg.exports['./workload'].import.default).toBe('./dist/workload.js');
    expect(pkg.exports['./workload'].require.default).toBe('./dist/workload.cjs');
    expect(pkg.exports['./workload'].import.types).toBe('./dist/workload.d.ts');
    expect(pkg.exports['./workload'].require.types).toBe('./dist/workload.d.cts');
    const core = readFileSync(join(root, 'src/index.ts'), 'utf8');
    expect(core).not.toMatch(/\.\/workload/);
  });

  it('exposes package.json, which tooling reads', () => {
    expect(pkg.exports['./package.json']).toBe('./package.json');
  });

  it('has a runnable example script', () => {
    expect(pkg.scripts.example).toBeDefined();
  });

  it('runs its tests once rather than watching', () => {
    // `"test": "vitest"` is watch mode: it never returns, in a terminal or in
    // CI. Watch mode is `test:watch`.
    expect(pkg.scripts.test).toBe('vitest run');
    expect(pkg.scripts['test:watch']).toBe('vitest');
  });

  it('declares ws as an optional peer', () => {
    // isomorphic-ws is `require("ws")` in Node and the browser WebSocket in a
    // browser, and it declares ws as a peer it does not install. The SDK
    // passed that requirement on to consumers without naming it, so a Node
    // consumer's pubsub failed to resolve ws at import. Optional, because a
    // browser consumer must not be made to install it.
    expect(pkg.peerDependencies.ws).toBeDefined();
    expect(pkg.peerDependenciesMeta.ws.optional).toBe(true);
  });
});

describe('the built package', () => {
  it.skipIf(!distBuilt)('can be required from CommonJS', () => {
    const dir = mkdtempSync(join(tmpdir(), 'orama-cjs-'));
    try {
      const file = join(dir, 'check.cjs');
      writeFileSync(
        file,
        `const sdk = require(${JSON.stringify(join(root, 'dist/index.cjs'))});\n` +
          `if (typeof sdk.createClient !== 'function') { process.exit(1); }\n`,
      );
      execFileSync(process.execPath, [file]);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it.skipIf(!distBuilt)('can be imported from ESM', () => {
    const dir = mkdtempSync(join(tmpdir(), 'orama-esm-'));
    try {
      const file = join(dir, 'check.mjs');
      writeFileSync(
        file,
        `import { createClient } from ${JSON.stringify(join(root, 'dist/index.js'))};\n` +
          `if (typeof createClient !== 'function') { process.exit(1); }\n`,
      );
      execFileSync(process.execPath, [file]);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it.skipIf(!distBuilt)('emits every file the exports map names', () => {
    for (const [name, entry] of Object.entries(pkg.exports)) {
      if (typeof entry === 'string') {
        continue;
      }
      const conditions = entry as Record<string, { types: string; default: string }>;
      for (const path of [
        conditions.import.default,
        conditions.import.types,
        conditions.require.default,
        conditions.require.types,
      ]) {
        expect(existsSync(join(root, path)), `${name}: ${path} is named in exports but not built`).toBe(true);
      }
    }
  });

  /**
   * Metro (React Native, Expo) resolves every import of a bundle, dynamic ones
   * included, and has no Node standard library: one `import("fs/promises")`
   * anywhere in the main entry's module graph fails the app's build. Walk that
   * graph — static imports, dynamic imports and requires, through every chunk
   * it reaches — and refuse any Node built-in.
   */
  it.skipIf(!distBuilt).each(['dist/index.js', 'dist/index.cjs'])(
    '%s reaches no Node built-in, so React Native and browsers can bundle it',
    (entry) => {
      const specifier =
        /(?:\bfrom\s*|\bimport\s*\(\s*|\brequire\s*\(\s*|^\s*import\s+)["']([^"']+)["']/gm;
      const builtins = new Set(builtinModules);
      const seen = new Set<string>();
      const offending: string[] = [];
      const visit = (file: string) => {
        if (seen.has(file)) {
          return;
        }
        seen.add(file);
        // Comments are not imports: the bundle keeps its JSDoc, examples and all.
        const source = readFileSync(file, 'utf8')
          .replace(/\/\*[\s\S]*?\*\//g, '')
          .replace(/^\s*\/\/.*$/gm, '');
        for (const match of source.matchAll(specifier)) {
          const spec = match[1];
          if (spec.startsWith('.')) {
            visit(resolve(dirname(file), spec));
          } else if (spec.startsWith('node:') || builtins.has(spec.split('/')[0]) || builtins.has(spec)) {
            offending.push(`${file.slice(root.length + 1)} imports ${spec}`);
          }
        }
      };
      visit(join(root, entry));
      expect(seen.size, 'the walk reached no chunk').toBeGreaterThan(1);
      expect(offending).toEqual([]);
    },
  );
});

describe('examples', () => {
  it('read the gateway from the environment', () => {
    // They hardcoded http://localhost:6001, which is not a port any gateway
    // has listened on since the port block moved to 10100.
    for (const name of ['basic-usage', 'database-crud', 'pubsub-chat']) {
      const source = readFileSync(join(root, `examples/${name}.ts`), 'utf8');
      expect(source, `${name} does not read GATEWAY_BASE_URL`).toContain('GATEWAY_BASE_URL');
      expect(source, `${name} still points at the retired :6001`).not.toContain('6001');
    }
  });
});

describe('the end-to-end suite', () => {
  /**
   * Every e2e file used to open with
   *
   *     if (skipIfNoGateway()) { console.log("Skipping ..."); }
   *
   * inside the describe block, which logs a line and then registers and runs
   * every test anyway. That is 27 of the suite's 30 failures on a checkout with
   * no gateway, while QUICKSTART.md said the tests skip gracefully.
   */
  const e2eFiles = readdirSync(join(root, 'tests/e2e')).filter((f) => f.endsWith('.test.ts'));

  it('has end-to-end files to check', () => {
    expect(e2eFiles.length).toBeGreaterThan(0);
  });

  it.each(e2eFiles)('%s skips itself when there is no gateway', (file) => {
    const source = readFileSync(join(root, 'tests/e2e', file), 'utf8');

    expect(source, `${file} does not gate its describe on a gateway`).toContain(
      'describe.skipIf(!hasGateway())',
    );
    expect(source, `${file} still has a describe that always runs`).not.toMatch(
      /^describe\(/m,
    );
  });
});

/**
 * The vault client moved to `@orama-network/orama-vault`. It talked to guardian
 * daemons on the WireGuard overlay, which an application outside the cluster
 * cannot reach, and it pulled two cryptography libraries and twenty top-level
 * primitives into every consumer's bundle for an API none of them could call.
 */
describe('the app SDK carries no vault', () => {
  it('declares no cryptography dependency', () => {
    // The chain entry (@orama-network/orama/chain) needs secp256k1 and protobuf. They
    // are optional peers a chain caller installs, never dependencies, so this
    // list, and every other client's install, stays free of them.
    const deps = Object.keys(pkg.dependencies ?? {});
    expect(deps.filter((d) => d.startsWith('@noble/'))).toEqual([]);
  });

  it('has no vault module in its source tree', () => {
    expect(existsSync(join(root, 'src/vault'))).toBe(false);
  });

  it('does not mention a vault in the client it builds', () => {
    const entry = readFileSync(join(root, 'src/index.ts'), 'utf8');
    expect(entry).not.toMatch(/vault/i);
  });

  it.skipIf(!distBuilt)('ships no cryptography code in the bundle', () => {
    for (const file of ['dist/index.js', 'dist/index.cjs']) {
      const bundle = readFileSync(join(root, file), 'utf8');
      expect(bundle, `${file} still contains shamir code`).not.toMatch(/shamir/i);
      expect(bundle, `${file} still bundles @noble`).not.toMatch(/@noble\//);
    }
  });

  it.skipIf(!distBuilt)('exports no vault symbol', () => {
    const dir = mkdtempSync(join(tmpdir(), 'orama-vault-exports-'));
    try {
      const script = join(dir, 'check.cjs');
      writeFileSync(
        script,
        `const sdk = require(${JSON.stringify(join(root, 'dist/index.cjs'))});\n` +
          `console.log(Object.keys(sdk).join(','));\n`,
      );
      const names = execFileSync(process.execPath, [script], { encoding: 'utf8' }).trim().split(',');
      const vaultish = names.filter((n) =>
        /vault|shamir|encrypt|decrypt|quorum|guardian|KEY_SIZE|NONCE_SIZE|TAG_SIZE|deriveKey/i.test(n),
      );
      expect(vaultish).toEqual([]);
      // Sanity: the check ran against a real module.
      expect(names).toContain('createClient');
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
