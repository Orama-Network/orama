import { defineConfig } from "tsup";

export default defineConfig({
  // The chain module is its own entry (@orama-network/orama/chain): its generated
  // protobuf codecs are large, and a caller that only uses the database or
  // pubsub clients should not carry them.
  entry: {
    index: "src/index.ts",
    chain: "src/chain/index.ts",
    // Node-only (node:tls): its own entry so the core stays bundleable for
    // React Native and browsers.
    relay: "src/storage/relay-transport.ts",
    // Node by default (it reads its token file with node:fs/promises): its own
    // entry so Metro, which resolves dynamic imports too, never meets it.
    workload: "src/workload.ts",
  },

  // Both module formats.
  //
  // The package was ESM-only while its README promised isomorphic use, so
  // `require("@orama-network/orama")` failed outright: Jest without ESM support,
  // ts-node in CJS mode, and CJS server code in a Next.js app all hit
  // ERR_REQUIRE_ESM at import. The exports map names the .cjs build under
  // "require" and the .js build under "import".
  format: ["esm", "cjs"],

  // tsup 8 injects the deprecated `baseUrl` into its declaration build;
  // TypeScript 6 rejects it unless the deprecation is acknowledged.
  dts: { compilerOptions: { ignoreDeprecations: "6.0" } },
  sourcemap: true,
  clean: true,
  splitting: true,

  // shims injects the import.meta.url / __dirname equivalents each format
  // lacks, so one source builds to both without conditional code.
  shims: true,

  outDir: "dist",
});
