//! The in-house bundle reader (src/wire.rs) against the one it replaced, zcash_primitives 0.30.1
//! `read_v6_bundle(_, BranchId::Nu6_3, ValuePool::Ironwood)` with every byte consumed.
//!
//! The property, on every input: both refuse, or both accept and build the same bundle (the same
//! bundle version and flags, and the same encoding under zcash_primitives' own writer). The inputs
//! are the real bundles in the repository (this crate's testdata and the F7 wallet scenario), every
//! single-bit flip and every truncation of them, the edge values of each header field, and seeded
//! random multi-byte mutations.

use std::fs;
use std::io::Cursor;
use std::path::Path;

use orama_orchard::wire::read_ironwood_bundle;
use orchard::bundle::Authorized;
use orchard::{Bundle, Proof, ValuePool};
use rand::rngs::StdRng;
use rand::{Rng, SeedableRng};
use zcash_primitives::transaction::components::orchard::{read_v6_bundle, write_v6_bundle};
use zcash_protocol::consensus::BranchId;
use zcash_protocol::value::{ZatBalance, MAX_BALANCE};

const ACTION_LEN: usize = 820;
const HEADER_LEN: usize = 41;
const RANDOM_MUTATIONS: usize = 4000;
const SEED: u64 = 0x0a5a_2026_0387;

const CRATE_VECTORS: [&str; 5] = [
    "ironwood-1-action",
    "ironwood-2-action",
    "ironwood-transfer",
    "ironwood-unshield",
    "ironwood-unshield-bond",
];
const WALLET_SCENARIO: &str = "../wallet/testdata/bundles/scenario.json";

/// What a reader built, in a form two readers can be compared by.
#[derive(Debug, PartialEq)]
struct Built {
    version: orchard::bundle::BundleVersion,
    flags: orchard::bundle::Flags,
    encoding: Vec<u8>,
}

fn built(bundle: &Bundle<Authorized, ZatBalance>) -> Built {
    let mut encoding = Vec::new();
    write_v6_bundle(Some(bundle), &mut encoding).unwrap();
    Built {
        version: bundle.bundle_version(),
        flags: *bundle.flags(),
        encoding,
    }
}

fn reference(data: &[u8]) -> Option<Built> {
    let mut cursor = Cursor::new(data);
    let bundle = read_v6_bundle(&mut cursor, BranchId::Nu6_3, ValuePool::Ironwood).ok()??;
    (cursor.position() as usize == data.len()).then(|| built(&bundle))
}

fn in_house(data: &[u8]) -> Option<Built> {
    read_ironwood_bundle(data).map(|b| built(&b))
}

/// Asserts the property on `data` and returns whether it was accepted.
fn agree(data: &[u8], what: &str) -> bool {
    let want = reference(data);
    let got = in_house(data);
    assert_eq!(got, want, "{what}: the readers differ on {} bytes", data.len());
    got.is_some()
}

fn hex_decode(s: &str) -> Vec<u8> {
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap())
        .collect()
}

/// Every real bundle in the repository: this crate's vectors and the wallet scenario's steps.
fn vectors() -> Vec<(String, Vec<u8>)> {
    let root = Path::new(env!("CARGO_MANIFEST_DIR"));
    let mut out: Vec<(String, Vec<u8>)> = CRATE_VECTORS
        .iter()
        .map(|n| {
            (
                n.to_string(),
                fs::read(root.join("testdata").join(format!("{n}.bundle"))).unwrap(),
            )
        })
        .collect();
    // The scenario's "bundle" fields are hex strings; the file has no other key of that name.
    let scenario = fs::read_to_string(root.join(WALLET_SCENARIO)).unwrap();
    let key = "\"bundle\": \"";
    for (i, (at, _)) in scenario.match_indices(key).enumerate() {
        let start = at + key.len();
        let end = start + scenario[start..].find('"').unwrap();
        out.push((format!("wallet-step-{i}"), hex_decode(&scenario[start..end])));
    }
    assert_eq!(
        out.len(),
        CRATE_VECTORS.len() + 4,
        "the wallet scenario has four steps"
    );
    out
}

/// Offsets of the fields after the actions: flags, value balance, anchor, the proof's CompactSize.
fn header_at(bundle: &[u8]) -> usize {
    1 + bundle[0] as usize * ACTION_LEN
}

#[test]
fn real_bundles_are_read_identically() {
    for (name, bundle) in vectors() {
        assert!(agree(&bundle, &name), "{name} is a valid bundle");
        assert_eq!(
            in_house(&bundle).unwrap().encoding,
            bundle,
            "{name}: re-encodes to its own bytes"
        );
    }
}

#[test]
fn every_bit_flip_is_read_identically() {
    for (name, bundle) in vectors() {
        for at in 0..bundle.len() {
            let mut b = bundle.clone();
            b[at] ^= 1 << (at % 8);
            agree(&b, &format!("{name}: bit {} of byte {at}", at % 8));
        }
    }
}

#[test]
fn every_truncation_and_extension_is_read_identically() {
    for (name, bundle) in vectors() {
        for len in 0..bundle.len() {
            assert!(!agree(&bundle[..len], &format!("{name}: first {len} bytes")));
        }
        let mut longer = bundle.clone();
        longer.push(0);
        assert!(!agree(&longer, &format!("{name}: one trailing byte")));
    }
}

#[test]
fn every_flag_byte_is_read_identically() {
    for (name, bundle) in vectors() {
        let at = header_at(&bundle);
        let accepted = (0..=255u8)
            .filter(|f| {
                let mut b = bundle.clone();
                b[at] = *f;
                agree(&b, &format!("{name}: flags {f:#04x}"))
            })
            .count();
        // Bits 0..=2 are the Ironwood v3 flags; bits 3..=7 are reserved.
        assert_eq!(accepted, 8, "{name}");
    }
}

#[test]
fn value_balance_edges_are_read_identically() {
    let edges = [
        0,
        1,
        -1,
        MAX_BALANCE,
        MAX_BALANCE + 1,
        -MAX_BALANCE,
        -MAX_BALANCE - 1,
        i64::MAX,
        i64::MIN,
    ];
    for (name, bundle) in vectors() {
        let at = header_at(&bundle) + 1;
        for v in edges {
            let mut b = bundle.clone();
            b[at..at + 8].copy_from_slice(&v.to_le_bytes());
            let accepted = agree(&b, &format!("{name}: value balance {v}"));
            assert_eq!(
                accepted,
                v.unsigned_abs() <= MAX_BALANCE as u64,
                "{name}: value balance {v}"
            );
        }
    }
}

/// The action count and the proof length re-encoded in every CompactSize width, canonical or not,
/// proofs of other lengths around the canonical one, and a bundle with no actions.
#[test]
fn compact_size_encodings_are_read_identically() {
    fn canonical(v: u64) -> Vec<u8> {
        let width = match v {
            0..=252 => 1,
            253..=0xffff => 3,
            0x1_0000..=0xffff_ffff => 5,
            _ => 9,
        };
        compact(width, v)
    }
    fn compact(width: u8, v: u64) -> Vec<u8> {
        match width {
            1 => vec![v as u8],
            3 => [&[253u8][..], &(v as u16).to_le_bytes()].concat(),
            5 => [&[254u8][..], &(v as u32).to_le_bytes()].concat(),
            _ => [&[255u8][..], &v.to_le_bytes()].concat(),
        }
    }
    for (name, bundle) in vectors() {
        let n = bundle[0] as usize;
        let actions = &bundle[1..header_at(&bundle)];
        let header = &bundle[header_at(&bundle)..header_at(&bundle) + HEADER_LEN];
        let proof_at = header_at(&bundle) + HEADER_LEN + 3;
        let proof_len = Proof::expected_proof_size(n);
        assert_eq!(
            bundle[proof_at - 3..proof_at],
            compact(3, proof_len as u64)[..],
            "{name}"
        );
        let proof = &bundle[proof_at..proof_at + proof_len];
        let sigs = &bundle[proof_at + proof_len..];

        for width in [1, 3, 5, 9] {
            let b = [
                compact(width, n as u64),
                actions.to_vec(),
                bundle[header_at(&bundle)..].to_vec(),
            ]
            .concat();
            agree(&b, &format!("{name}: action count in {width} bytes"));
            let b = [
                &bundle[..proof_at - 3],
                &compact(width, proof_len as u64)[..],
                proof,
                sigs,
            ]
            .concat();
            agree(&b, &format!("{name}: proof length in {width} bytes"));
        }
        for len in [0, 1, proof_len - 1, proof_len + 1] {
            let mut body = proof.to_vec();
            body.resize(len, 0);
            let b = [&bundle[..proof_at - 3], &canonical(len as u64)[..], &body, sigs].concat();
            assert!(!agree(&b, &format!("{name}: proof length {len}")));
        }
        assert!(!agree(&[0u8], "no actions"));
        assert!(!agree(
            &[&[0u8][..], header, &[0u8][..]].concat(),
            "no actions, then a header"
        ));
    }
}

/// A proof as long as a Zcash vector may be (0x0200_0000 bytes), and one byte longer than that.
/// Both are refused: the first for its size against the action count, the second by the bound.
#[test]
fn proof_at_the_vector_bound_is_read_identically() {
    let (name, bundle) = &vectors()[0];
    let proof_at = header_at(bundle) + HEADER_LEN + 3;
    let sigs = &bundle[proof_at + Proof::expected_proof_size(bundle[0] as usize)..];
    for len in [0x0200_0000u32, 0x0200_0001] {
        let body = vec![0u8; len as usize];
        let b = [
            &bundle[..proof_at - 3],
            &[254u8][..],
            &len.to_le_bytes(),
            &body,
            sigs,
        ]
        .concat();
        assert!(!agree(&b, &format!("{name}: proof length {len:#x}")));
    }
}

/// Seeded random mutations: a few bytes overwritten, a range zeroed or set to 0xff, a slice moved.
#[test]
fn random_mutations_are_read_identically() {
    let mut rng = StdRng::seed_from_u64(SEED);
    let vectors = vectors();
    for i in 0..RANDOM_MUTATIONS {
        let (name, bundle) = &vectors[i % vectors.len()];
        let mut b = bundle.clone();
        match rng.gen_range(0..4) {
            0 => {
                for _ in 0..rng.gen_range(1..8) {
                    let at = rng.gen_range(0..b.len());
                    b[at] = rng.gen();
                }
            }
            1 | 2 => {
                let start = rng.gen_range(0..b.len());
                let end = rng.gen_range(start..b.len().min(start + 64) + 1);
                let fill = if rng.gen() { 0 } else { 0xff };
                b[start..end].fill(fill);
            }
            _ => {
                let len = rng.gen_range(1..64);
                let from = rng.gen_range(0..b.len() - len);
                let to = rng.gen_range(0..b.len() - len);
                let chunk = b[from..from + len].to_vec();
                b[to..to + len].copy_from_slice(&chunk);
            }
        }
        agree(&b, &format!("{name}: mutation {i}"));
    }
}
