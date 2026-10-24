//! The fork against upstream: secp256k1 and secp256r1 verify and public-key recovery must give the
//! same value, or fail with the same error code, on every input.
//!
//! Inputs are seeded and deterministic. Each round signs a hash with a fresh key on both curves and
//! then, with one mutation picked at random, checks verify on the (mutated) signature and key and
//! recovery under the signed recovery id or another one, and under every recovery id 0..=4 against
//! the uncompressed key. The mutations cover what the two curve-crate generations could disagree on:
//! high-S twins, r and s at 0, 1, n-1, n, n+1 and 2^256-1, an x-reduced r for recovery ids 2 and 3,
//! keys off the curve, hybrid (0x06/0x07) and truncated keys, x = p, and inputs of a wrong length.

use rand::rngs::StdRng;
use rand::{Rng, RngCore, SeedableRng};

/// Rounds per run. Each round checks 12 cases (6 per curve), plus 4 wrong-length cases every
/// `WRONG_LENGTH_EVERY` rounds.
const ROUNDS: u64 = 4000;
const WRONG_LENGTH_EVERY: u64 = 101;
const SEED: u64 = 0x4110_0387;

/// The group orders and field primes, big-endian.
const N_K1: [u8; 32] = hex32("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141");
const P_K1: [u8; 32] = hex32("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F");
const N_R1: [u8; 32] = hex32("FFFFFFFF00000000FFFFFFFFFFFFFFFFBCE6FAADA7179E84F3B9CAC2FC632551");
const P_R1: [u8; 32] = hex32("FFFFFFFF00000001000000000000000000000000FFFFFFFFFFFFFFFFFFFFFFFF");

const fn hex32(s: &str) -> [u8; 32] {
    const fn nibble(c: u8) -> u8 {
        match c {
            b'0'..=b'9' => c - b'0',
            b'A'..=b'F' => c - b'A' + 10,
            _ => panic!("not an upper-case hex digit"),
        }
    }
    let b = s.as_bytes();
    let mut out = [0u8; 32];
    let mut i = 0;
    while i < 32 {
        out[i] = (nibble(b[2 * i]) << 4) | nibble(b[2 * i + 1]);
        i += 1;
    }
    out
}

/// a - b over 256-bit big-endian integers, wrapping.
fn sub(a: &[u8; 32], b: &[u8; 32]) -> [u8; 32] {
    let mut out = [0u8; 32];
    let mut borrow = 0i16;
    for j in (0..32).rev() {
        let v = a[j] as i16 - b[j] as i16 - borrow;
        out[j] = v.rem_euclid(256) as u8;
        borrow = i16::from(v < 0);
    }
    out
}

fn small(k: u8) -> [u8; 32] {
    let mut out = [0u8; 32];
    out[31] = k;
    out
}

/// a + k, wrapping.
fn add_small(a: &[u8; 32], k: u8) -> [u8; 32] {
    sub(a, &sub(&[0u8; 32], &small(k)))
}

#[derive(Clone, Copy, PartialEq)]
enum Curve {
    K1,
    R1,
}

#[derive(Debug, PartialEq)]
enum Out {
    Bool(bool),
    Key(Vec<u8>),
    Err(u32),
}

fn verify_both(curve: Curve, hash: &[u8], sig: &[u8], key: &[u8]) -> (Out, Out) {
    let (up, fork) = match curve {
        Curve::K1 => (upstream::secp256k1_verify(hash, sig, key), fork::secp256k1_verify(hash, sig, key)),
        Curve::R1 => (upstream::secp256r1_verify(hash, sig, key), fork::secp256r1_verify(hash, sig, key)),
    };
    (
        up.map_or_else(|e| Out::Err(e.code()), Out::Bool),
        fork.map_or_else(|e| Out::Err(e.code()), Out::Bool),
    )
}

fn recover_both(curve: Curve, hash: &[u8], sig: &[u8], id: u8) -> (Out, Out) {
    let (up, fork) = match curve {
        Curve::K1 => (
            upstream::secp256k1_recover_pubkey(hash, sig, id),
            fork::secp256k1_recover_pubkey(hash, sig, id),
        ),
        Curve::R1 => (
            upstream::secp256r1_recover_pubkey(hash, sig, id),
            fork::secp256r1_recover_pubkey(hash, sig, id),
        ),
    };
    (
        up.map_or_else(|e| Out::Err(e.code()), Out::Key),
        fork.map_or_else(|e| Out::Err(e.code()), Out::Key),
    )
}

#[derive(Default)]
struct Tally {
    cases: u64,
    verified: u64,
    refused: u64,
    errors: u64,
    recovered: u64,
}

impl Tally {
    fn check(&mut self, curve: Curve, hash: &[u8], sig: &[u8], key: &[u8], id: u8) {
        self.cases += 1;
        let (up, fork) = verify_both(curve, hash, sig, key);
        assert_eq!(up, fork, "verify: hash {hash:02x?} sig {sig:02x?} key {key:02x?}");
        match up {
            Out::Bool(true) => self.verified += 1,
            Out::Bool(false) => self.refused += 1,
            _ => self.errors += 1,
        }
        let (up, fork) = recover_both(curve, hash, sig, id);
        assert_eq!(up, fork, "recover: hash {hash:02x?} sig {sig:02x?} id {id}");
        if let Out::Key(_) = up {
            self.recovered += 1;
        }
    }
}

/// A real signature over `hash`: (signature, recovery id, compressed key, uncompressed key).
fn sign(curve: Curve, secret: &[u8; 32], hash: &[u8; 32]) -> ([u8; 64], u8, Vec<u8>, Vec<u8>) {
    match curve {
        Curve::K1 => {
            let key = k256::ecdsa::SigningKey::from_slice(secret).unwrap();
            let (sig, id) = key.sign_prehash_recoverable(hash).unwrap();
            let vk = key.verifying_key();
            let (c, u) = (vk.to_encoded_point(true), vk.to_encoded_point(false));
            (sig.to_bytes().into(), id.to_byte(), c.as_bytes().to_vec(), u.as_bytes().to_vec())
        }
        Curve::R1 => {
            let key = p256::ecdsa::SigningKey::from_slice(secret).unwrap();
            let (sig, id) = key.sign_prehash_recoverable(hash).unwrap();
            let vk = key.verifying_key();
            let (c, u) = (vk.to_encoded_point(true), vk.to_encoded_point(false));
            (sig.to_bytes().into(), id.to_byte(), c.as_bytes().to_vec(), u.as_bytes().to_vec())
        }
    }
}

#[test]
fn fork_matches_upstream_on_every_ecdsa_entry_point() {
    let mut rng = StdRng::seed_from_u64(SEED);
    let mut tally = Tally::default();
    for round in 0..ROUNDS {
        for curve in [Curve::K1, Curve::R1] {
            let (n, p) = match curve {
                Curve::K1 => (N_K1, P_K1),
                Curve::R1 => (N_R1, P_R1),
            };
            let mut hash = [0u8; 32];
            rng.fill_bytes(&mut hash);
            if round % 17 == 0 {
                hash = [0u8; 32];
            }
            if round % 19 == 0 {
                hash = [0xff; 32];
            }
            if round % 23 == 0 {
                hash = n;
            }
            let mut secret = [0u8; 32];
            rng.fill_bytes(&mut secret);
            secret[0] &= 0x7f;
            secret[31] |= 1;
            let (mut sig, id, compressed, uncompressed) = sign(curve, &secret, &hash);
            let mut key = if rng.gen_bool(0.5) { compressed } else { uncompressed.clone() };
            let edges = [[0u8; 32], small(1), sub(&n, &small(1)), n, add_small(&n, 1), [0xff; 32]];

            match rng.gen_range(0..16) {
                0 => {}
                1 => {
                    let s: [u8; 32] = sig[32..].try_into().unwrap();
                    sig[32..].copy_from_slice(&sub(&n, &s));
                }
                2 => sig[rng.gen_range(0..64)] ^= 1 << rng.gen_range(0..8),
                3 => {
                    let at = rng.gen_range(0..key.len());
                    key[at] ^= 1 << rng.gen_range(0..8);
                }
                4 => sig[..32].copy_from_slice(&edges[rng.gen_range(0..edges.len())]),
                5 => sig[32..].copy_from_slice(&edges[rng.gen_range(0..edges.len())]),
                6 => rng.fill_bytes(&mut sig),
                7 => {
                    let prefix = key[0];
                    rng.fill_bytes(&mut key);
                    key[0] = prefix;
                }
                8 => {
                    key = uncompressed.clone();
                    key[0] = 0x06 + (uncompressed[64] & 1);
                }
                9 => {
                    key = vec![0x02; 33];
                    key[1..].copy_from_slice(&p);
                }
                10 => {
                    let mut r = [0u8; 32];
                    r[31] = rng.gen_range(1..=255);
                    r[30] = rng.gen();
                    sig[..32].copy_from_slice(&r);
                }
                11 => {
                    let near_p = sub(&p, &small(rng.gen_range(0..=9)));
                    sig[..32].copy_from_slice(&sub(&near_p, &n));
                }
                12 => {
                    key = vec![0u8; 65];
                    key[0] = 0x04;
                }
                13 => key.truncate(key.len() - 1),
                14 => hash[rng.gen_range(0..32)] ^= 1,
                _ => key = vec![0x00],
            }
            let recovery_id = if rng.gen_bool(0.7) { id } else { rng.gen_range(0..=5) };
            tally.check(curve, &hash, &sig, &key, recovery_id);
            for any_id in 0..=4u8 {
                tally.check(curve, &hash, &sig, &uncompressed, any_id);
            }
            if round % WRONG_LENGTH_EVERY == 0 {
                tally.check(curve, &hash[..31], &sig, &key, recovery_id);
                tally.check(curve, &hash, &sig[..63], &key, recovery_id);
            }
        }
    }
    // Every class of answer was reached, so no branch of the comparison went untested.
    assert!(tally.verified > 0 && tally.refused > 0 && tally.errors > 0 && tally.recovered > 0);
    println!(
        "{} cases: verify true {}, false {}, error {}; recovered {}",
        tally.cases, tally.verified, tally.refused, tally.errors, tally.recovered
    );
}
