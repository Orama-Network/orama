//! Verify-only C ABI over upstream `orchard` for Ironwood bundles.
//!
//! The verifying key is the post-NU6.3 key and nothing else. The insecure pre-NU6.2 circuit is
//! never built here. There is no prover in this crate: proofs are built by wallets.

pub mod tree;
pub mod wire;

use std::panic::{catch_unwind, AssertUnwindSafe};
use std::slice;
use std::sync::OnceLock;

use orchard::circuit::{OrchardCircuitVersion, VerifyingKey};
use orchard::Proof;

use wire::{read_compact_size, read_ironwood_bundle, ACTION_LEN, BUNDLE_HEADER_LEN};

pub const OK: i32 = 0;
pub const MALFORMED: i32 = 1;
pub const BAD_PROOF_LENGTH: i32 = 2;
pub const PROOF_REJECTED: i32 = 3;
pub const SIGNATURE_REJECTED: i32 = 4;
pub const PANIC: i32 = 5;
pub const BAD_ARGUMENT: i32 = 6;

static VERIFYING_KEY: OnceLock<VerifyingKey> = OnceLock::new();

fn verifying_key() -> &'static VerifyingKey {
    VERIFYING_KEY.get_or_init(|| VerifyingKey::build(OrchardCircuitVersion::PostNu6_3))
}

/// Checks the framing and the proof length before any cryptography runs. The proof must be
/// exactly `Proof::expected_proof_size(n_actions)` bytes.
fn check_framing(data: &[u8]) -> Result<(), i32> {
    let mut pos = 0;
    let n = read_compact_size(data, &mut pos).ok_or(MALFORMED)?;
    let n = usize::try_from(n).map_err(|_| MALFORMED)?;
    if n == 0 {
        return Err(MALFORMED);
    }
    let after_actions = n
        .checked_mul(ACTION_LEN)
        .and_then(|a| a.checked_add(BUNDLE_HEADER_LEN))
        .and_then(|a| a.checked_add(pos))
        .ok_or(MALFORMED)?;
    if after_actions > data.len() {
        return Err(MALFORMED);
    }
    pos = after_actions;
    let proof_len = read_compact_size(data, &mut pos).ok_or(MALFORMED)?;
    if proof_len != Proof::expected_proof_size(n) as u64 {
        return Err(BAD_PROOF_LENGTH);
    }
    Ok(())
}

/// Verifies `data` against `sighash`. Returns one of the result codes.
pub fn verify(data: &[u8], sighash: &[u8; 32]) -> i32 {
    if let Err(code) = check_framing(data) {
        return code;
    }
    let Some(bundle) = read_ironwood_bundle(data) else {
        return MALFORMED;
    };
    for action in bundle.actions() {
        if action.rk().verify(sighash, action.authorization()).is_err() {
            return SIGNATURE_REJECTED;
        }
    }
    let binding = bundle.authorization().binding_signature();
    if bundle
        .binding_validating_key()
        .verify(sighash, binding)
        .is_err()
    {
        return SIGNATURE_REJECTED;
    }
    match bundle.verify_proof(verifying_key()) {
        Ok(()) => OK,
        Err(_) => PROOF_REJECTED,
    }
}

/// C entry point. See `include/orama_orchard.h`.
///
/// # Safety
/// `bundle` must point to `bundle_len` readable bytes and `sighash` to 32 readable bytes.
#[no_mangle]
pub unsafe extern "C" fn orama_orchard_verify(
    bundle: *const u8,
    bundle_len: usize,
    sighash: *const u8,
) -> i32 {
    if bundle.is_null() || sighash.is_null() || bundle_len == 0 {
        return BAD_ARGUMENT;
    }
    let data = slice::from_raw_parts(bundle, bundle_len);
    let mut hash = [0u8; 32];
    hash.copy_from_slice(slice::from_raw_parts(sighash, 32));
    catch_unwind(AssertUnwindSafe(|| verify(data, &hash))).unwrap_or(PANIC)
}

/// C entry point. Builds the verifying key so the first bundle does not pay for it.
#[no_mangle]
pub extern "C" fn orama_orchard_warm() -> i32 {
    catch_unwind(|| {
        verifying_key();
        OK
    })
    .unwrap_or(PANIC)
}

/// C entry point for the frontier. See `include/orama_orchard.h`.
///
/// # Safety
/// Every pointer must be valid for its length (`frontier` may be null only when
/// `frontier_len` is 0, `commitments` only when `n_commitments` is 0), `out_frontier` for
/// `out_cap` writable bytes, `out_len` for one `usize` and `out_root` for 32 writable bytes.
#[no_mangle]
pub unsafe extern "C" fn orama_orchard_tree_append(
    frontier: *const u8,
    frontier_len: usize,
    commitments: *const u8,
    n_commitments: usize,
    out_frontier: *mut u8,
    out_cap: usize,
    out_len: *mut usize,
    out_root: *mut u8,
) -> i32 {
    if (frontier.is_null() && frontier_len != 0)
        || (commitments.is_null() && n_commitments != 0)
        || out_frontier.is_null()
        || out_len.is_null()
        || out_root.is_null()
    {
        return BAD_ARGUMENT;
    }
    let Some(commitment_bytes) = n_commitments.checked_mul(tree::NODE_LEN) else {
        return BAD_ARGUMENT;
    };
    let frontier_in: &[u8] = if frontier_len == 0 { &[] } else { slice::from_raw_parts(frontier, frontier_len) };
    let cmx: &[u8] = if n_commitments == 0 { &[] } else { slice::from_raw_parts(commitments, commitment_bytes) };
    let result = catch_unwind(AssertUnwindSafe(|| tree::append(frontier_in, cmx)));
    match result {
        Err(_) => PANIC,
        Ok(None) => MALFORMED,
        Ok(Some((frontier_out, root))) => {
            if frontier_out.len() > out_cap {
                return BAD_ARGUMENT;
            }
            slice::from_raw_parts_mut(out_frontier, frontier_out.len()).copy_from_slice(&frontier_out);
            *out_len = frontier_out.len();
            slice::from_raw_parts_mut(out_root, tree::NODE_LEN).copy_from_slice(&root);
            OK
        }
    }
}

/// C entry point for the root of a filled 2^16-leaf subtree. See `include/orama_orchard.h`.
///
/// # Safety
/// `frontier` must be valid for `frontier_len` bytes and `out_root` for 32 writable bytes.
#[no_mangle]
pub unsafe extern "C" fn orama_orchard_tree_subtree_root(
    frontier: *const u8,
    frontier_len: usize,
    out_root: *mut u8,
) -> i32 {
    if frontier.is_null() || frontier_len == 0 || out_root.is_null() {
        return BAD_ARGUMENT;
    }
    let frontier_in = slice::from_raw_parts(frontier, frontier_len);
    match catch_unwind(AssertUnwindSafe(|| tree::subtree_root(frontier_in))) {
        Err(_) => PANIC,
        Ok(None) => MALFORMED,
        Ok(Some(root)) => {
            slice::from_raw_parts_mut(out_root, tree::NODE_LEN).copy_from_slice(&root);
            OK
        }
    }
}
