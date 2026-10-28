import { describe, expect, it } from 'vitest';
import {
  AuthCode,
  AuthError,
  FunctionCode,
  FunctionNotDirectlyInvocableError,
  NetworkError,
  NotFoundError,
  RevokedCredentialError,
  ScopeError,
  SDKError,
} from '../../src/errors';

/**
 * There was one error class for everything, so the only way to tell a rejected
 * credential from a missing row from an unreachable gateway was to compare
 * numbers. These four are the cases an application actually handles
 * differently. `SDKError` stays the base, so an existing `instanceof SDKError`
 * still catches all of them.
 */

describe('SDKError.fromResponse', () => {
  // bugboard #414: endpoints that answer with the RPC envelope used to surface
  // as message "[object Object]" and code "HTTP_<status>".
  it('unwraps the RPC error envelope', () => {
    const error = SDKError.fromResponse(404, {
      ok: false,
      error: { code: 'NOT_FOUND', message: 'content Qm is not stored', retryable: false },
    });
    expect(error).toBeInstanceOf(NotFoundError);
    expect(error.message).toBe('content Qm is not stored');
    expect(error.code).toBe('NOT_FOUND');
    expect(error.retryable).toBe(false);
  });

  it('reports retryable only when the gateway said so', () => {
    expect(
      SDKError.fromResponse(504, { ok: false, error: { code: 'TIMEOUT', message: 't', retryable: true } }).retryable
    ).toBe(true);
    expect(SDKError.fromResponse(500, { error: 'boom' }).retryable).toBeUndefined();
  });

  it('returns an AuthError for 401', () => {
    const error = SDKError.fromResponse(401, { error: 'token expired' });
    expect(error).toBeInstanceOf(AuthError);
    expect(error).toBeInstanceOf(SDKError);
    expect(error.httpStatus).toBe(401);
    expect(error.message).toBe('token expired');
  });

  it('returns a ScopeError for 403', () => {
    const error = SDKError.fromResponse(403, { error: 'nope' });
    expect(error).toBeInstanceOf(ScopeError);
    expect(error).toBeInstanceOf(SDKError);
  });

  // feat-971: a function only its parents may invoke is refused by name; no
  // grant opens it, so it is not a ScopeError.
  it('returns a FunctionNotDirectlyInvocableError for 403 FUNCTION_NOT_DIRECTLY_INVOCABLE', () => {
    const error = SDKError.fromResponse(403, {
      ok: false,
      error: { code: 'FUNCTION_NOT_DIRECTLY_INVOCABLE', message: 'runs only when rpc-router calls it', retryable: false },
    });
    expect(error).toBeInstanceOf(FunctionNotDirectlyInvocableError);
    expect(error).not.toBeInstanceOf(ScopeError);
    expect(error.code).toBe(FunctionCode.NotDirectlyInvocable);
    expect(error.retryable).toBe(false);
  });

  it('returns a NotFoundError for 404', () => {
    expect(SDKError.fromResponse(404, { error: 'gone' })).toBeInstanceOf(NotFoundError);
  });

  it('returns a plain SDKError for everything else', () => {
    const error = SDKError.fromResponse(500, { error: 'boom' });
    expect(error).toBeInstanceOf(SDKError);
    expect(error).not.toBeInstanceOf(AuthError);
    expect(error).not.toBeInstanceOf(ScopeError);
    expect(error).not.toBeInstanceOf(NotFoundError);
  });

  it('keeps the gateway code when it sends one', () => {
    const error = SDKError.fromResponse(403, {
      error: 'insufficient scope',
      code: 'INSUFFICIENT_SCOPE',
    });
    expect(error.code).toBe('INSUFFICIENT_SCOPE');
  });

  it('falls back to the status when the gateway sends no code', () => {
    expect(SDKError.fromResponse(502, { error: 'bad gateway' }).code).toBe('HTTP_502');
  });

  it('keeps the whole body in details', () => {
    const error = SDKError.fromResponse(403, {
      error: 'insufficient scope',
      code: 'INSUFFICIENT_SCOPE',
      required_scope: 'storage',
    });
    expect(error.details.required_scope).toBe('storage');
  });
});

describe('ScopeError', () => {
  /**
   * The grant comes from the gateway's `required_scope` field. Before it had
   * one, a client that wanted to say "ask for a key with storage" had to match
   * the English sentence.
   */
  it('names the grant the operation needs', () => {
    const error = SDKError.fromResponse(403, {
      error: "insufficient scope: this credential lacks the 'storage' grant required for /v1/storage/upload",
      code: 'INSUFFICIENT_SCOPE',
      required_scope: 'storage',
    }) as ScopeError;

    expect(error.requiredScope).toBe('storage');
  });

  it('has no grant when the gateway named none', () => {
    const error = SDKError.fromResponse(403, { error: 'forbidden' }) as ScopeError;
    expect(error.requiredScope).toBeUndefined();
  });
});

describe('AuthError', () => {
  it('carries the grant on a layer-1 refusal, which is a 401', () => {
    const error = SDKError.fromResponse(401, {
      error: 'user authentication required (JWT)',
      code: 'USER_JWT_REQUIRED',
      required_scope: 'proxy',
    }) as AuthError;

    expect(error).toBeInstanceOf(AuthError);
    expect(error.code).toBe('USER_JWT_REQUIRED');
    expect(error.requiredScope).toBe('proxy');
  });
});

describe('NetworkError', () => {
  it('always reports status 0, which is how "never reached" is recognised', () => {
    const error = new NetworkError('connection refused');
    expect(error.httpStatus).toBe(0);
    expect(error.code).toBe('NETWORK_ERROR');
    expect(error).toBeInstanceOf(SDKError);
  });

  it('carries a more specific code when there is one', () => {
    expect(new NetworkError('timed out', 'TIMEOUT').code).toBe('TIMEOUT');
    expect(new NetworkError('cancelled', 'ABORTED').code).toBe('ABORTED');
  });
});

describe('every error', () => {
  it('serialises to something loggable', () => {
    const error = SDKError.fromResponse(404, { error: 'no such row' });
    expect(error.toJSON()).toMatchObject({
      name: 'NotFoundError',
      message: 'no such row',
      httpStatus: 404,
    });
  });

  it('keeps its class name, so a log says which kind it was', () => {
    expect(SDKError.fromResponse(401, {}).name).toBe('AuthError');
    expect(SDKError.fromResponse(403, {}).name).toBe('ScopeError');
    expect(SDKError.fromResponse(404, {}).name).toBe('NotFoundError');
    expect(new NetworkError('x').name).toBe('NetworkError');
  });
});

/**
 * A 401 had at least six causes and told them apart only by an English string,
 * so nothing could distinguish "you sent nothing" from "your key was revoked"
 * without matching on prose. Every refusal carries a code now, and the one an
 * application has to handle differently — the credential is gone, sign in
 * again — gets its own class.
 */
describe('auth codes', () => {
  it('gives a revoked credential its own error', () => {
    const error = SDKError.fromResponse(401, {
      error: 'this session was ended',
      code: AuthCode.Revoked,
      hint: 'this credential or session was revoked; sign in again or use a new key',
    });

    expect(error).toBeInstanceOf(RevokedCredentialError);
    expect(error).toBeInstanceOf(AuthError);
    expect(error).toBeInstanceOf(SDKError);
    expect(error.code).toBe('AUTH_REVOKED');
  });

  it('keeps a plain AuthError for the other 401 causes', () => {
    for (const code of [AuthCode.Missing, AuthCode.InvalidKey, AuthCode.Expired]) {
      const error = SDKError.fromResponse(401, { error: 'nope', code });
      expect(error).toBeInstanceOf(AuthError);
      expect(error).not.toBeInstanceOf(RevokedCredentialError);
      expect(error.code).toBe(code);
    }
  });

  it('surfaces the hint the gateway sent', () => {
    const error = SDKError.fromResponse(401, {
      error: 'no credential was presented',
      code: AuthCode.Missing,
      hint: 'send the credential in an Authorization header, or X-API-Key',
    });
    expect(error.hint).toContain('Authorization');
  });

  it('has no hint when the gateway sent none', () => {
    expect(SDKError.fromResponse(500, { error: 'boom' }).hint).toBeUndefined();
  });

  it('names the grant a scope refusal requires without parsing the message', () => {
    const error = SDKError.fromResponse(403, {
      error: "insufficient scope: this credential lacks the 'admin' grant required for /v1/deployments/list",
      code: AuthCode.ScopeMissing,
      required_scope: 'admin',
    });
    expect(error).toBeInstanceOf(ScopeError);
    expect((error as ScopeError).requiredScope).toBe('admin');
  });

  it('carries the namespaces on a mismatch', () => {
    const error = SDKError.fromResponse(403, {
      error: 'this credential belongs to another namespace',
      code: AuthCode.NamespaceMismatch,
      namespace: 'alice',
      credential_namespace: 'bob',
    });
    expect(error.code).toBe('NAMESPACE_MISMATCH');
    expect(error.details.credential_namespace).toBe('bob');
  });
});
