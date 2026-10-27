import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { AuthCode } from '../../src/errors';

/**
 * `AuthCode` is the contract for refusals: the gateway puts one on every 401
 * and 403, and an application switches on it instead of matching English prose.
 * A code the gateway can send and this list does not name is a refusal an
 * application cannot branch on — which is the whole problem the codes were
 * added to solve, reintroduced silently.
 *
 * So the list is checked against the Go source rather than trusted.
 */

const goSources = [
  resolve(__dirname, '../../../core/pkg/gateway/auth_errors.go'),
  resolve(__dirname, '../../../core/pkg/gateway/handlers/auth/signin_errors.go'),
  resolve(__dirname, '../../../core/pkg/gateway/handlers/auth/device_errors.go'),
  resolve(__dirname, '../../../core/pkg/gateway/handlers/attest/errors.go'),
];
const haveGo = goSources.every((p) => existsSync(p));
const go = haveGo ? goSources.map((p) => readFileSync(p, 'utf8')).join('\n') : '';

/** The value of every `Code<Name> = "VALUE"` / `ErrCode<Name> = "VALUE"` constant. */
function declaredCodes(): string[] {
  const codes = [...go.matchAll(/(?:Err)?Code\w+\s+=\s+"([A-Z_]+)"/g)].map((m) => m[1]);
  return [...new Set(codes)].sort();
}

describe('auth error codes match the gateway', () => {
  it('finds the gateway sources', () => {
    expect(haveGo, `${goSources.join(', ')} — this test cannot check parity`).toBe(true);
  });

  it.skipIf(!haveGo)('reads a plausible number of codes out of the Go source', () => {
    expect(declaredCodes().length).toBeGreaterThan(10);
  });

  it.skipIf(!haveGo)('names every code the gateway can send', () => {
    const known = new Set<string>(Object.values(AuthCode));
    // NAMESPACE_NOT_OWNED and NAMESPACE_UNKNOWN are answers about a namespace
    // rather than about the caller's credential, and they carry the namespace
    // in their own field; TOO_MANY_CHALLENGES is a rate limit. They are not
    // AuthCode values, and are listed here so that a code arriving from the
    // gateway is either in AuthCode or deliberately not.
    const notCredentialRefusals = new Set([
      'NAMESPACE_NOT_OWNED',
      'NAMESPACE_UNKNOWN',
      'TOO_MANY_CHALLENGES',
    ]);

    const missing = declaredCodes().filter(
      (code) => !known.has(code) && !notCredentialRefusals.has(code)
    );
    expect(missing, 'codes the gateway sends that the SDK cannot name').toEqual([]);
  });

  it.skipIf(!haveGo)('claims no code the gateway does not send', () => {
    const declared = new Set(declaredCodes());
    const invented = Object.values(AuthCode).filter((code) => !declared.has(code));
    expect(invented, 'codes the SDK names that no gateway constant declares').toEqual([]);
  });
});
