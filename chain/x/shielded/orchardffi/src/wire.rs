//! Reader for the Ironwood bundle of a v6 transaction, the encoding the chain carries.
//!
//! It accepts and rejects the same bytes, and builds the same `Bundle`, as the reader it replaces:
//! `zcash_primitives` 0.30.1 `read_v6_bundle(_, BranchId::Nu6_3, ValuePool::Ironwood)` followed by a
//! check that every byte was read. Every element is decoded by `orchard`'s own constructor, so the
//! canonical-encoding rules (field elements, curve points, the flag byte, the proof size) are
//! upstream's; this module only walks the layout. `zcash_primitives` is not a dependency because it
//! pins pre-release RustCrypto crates (crypto-common 0.2.0-rc.1, digest 0.11.0-pre.9) that cannot
//! share a dependency graph with the stable ones the CosmWasm half of `chain/native` needs.
//!
//! The layout, in order:
//!
//! ```text
//! CompactSize n             number of actions, at least 1
//! n × action                cv_net, nf, rk, cmx, epk (32 bytes each), enc (580), out (80)
//! flags                     1 byte, under the Ironwood v3 grammar
//! value_balance             8 bytes, signed little-endian, within ±MAX_MONEY
//! anchor                    32 bytes
//! CompactSize len, proof    len bytes, exactly the canonical size for n actions
//! n × spend auth signature  64 bytes each
//! binding signature         64 bytes
//! ```

use nonempty::NonEmpty;
use orchard::bundle::{Authorized, BundleVersion, Flags};
use orchard::note::{ExtractedNoteCommitment, Nullifier, TransmittedNoteCiphertext};
use orchard::primitives::redpallas::{SigType, Signature, SpendAuth, VerificationKey};
use orchard::value::ValueCommitment;
use orchard::{Action, Anchor, Bundle, Proof};
use zcash_protocol::value::ZatBalance;

/// The largest CompactSize a Zcash vector length may take (`zcash_encoding::MAX_COMPACT_SIZE`).
const MAX_COMPACT_SIZE: u64 = 0x0200_0000;
const ELEMENT_LEN: usize = 32;
const ENC_CIPHERTEXT_LEN: usize = 580;
const OUT_CIPHERTEXT_LEN: usize = 80;
const SIGNATURE_LEN: usize = 64;
const VALUE_BALANCE_LEN: usize = 8;
/// Bytes in one action without its signature: five elements and the two ciphertexts.
pub(crate) const ACTION_LEN: usize = 5 * ELEMENT_LEN + ENC_CIPHERTEXT_LEN + OUT_CIPHERTEXT_LEN;
/// flags, value balance and anchor: the bytes between the actions and the proof.
pub(crate) const BUNDLE_HEADER_LEN: usize = 1 + VALUE_BALANCE_LEN + ELEMENT_LEN;

/// Reads a Bitcoin-style CompactSize at `*pos`, canonically encoded only. No upper bound: the
/// framing check compares it with sizes of its own, and [`Reader::vector_len`] adds Zcash's.
pub(crate) fn read_compact_size(data: &[u8], pos: &mut usize) -> Option<u64> {
    let first = *data.get(*pos)?;
    *pos += 1;
    let (width, min) = match first {
        0..=252 => return Some(first as u64),
        253 => (2, 253),
        254 => (4, 0x1_0000),
        255 => (8, 0x1_0000_0000),
    };
    let raw = data.get(*pos..pos.checked_add(width)?)?;
    *pos += width;
    let mut value = 0u64;
    for (i, b) in raw.iter().enumerate() {
        value |= (*b as u64) << (8 * i);
    }
    (value >= min).then_some(value)
}

struct Reader<'a> {
    data: &'a [u8],
    pos: usize,
}

impl<'a> Reader<'a> {
    fn bytes(&mut self, len: usize) -> Option<&'a [u8]> {
        let out = self.data.get(self.pos..self.pos.checked_add(len)?)?;
        self.pos += len;
        Some(out)
    }

    fn array<const N: usize>(&mut self) -> Option<[u8; N]> {
        self.bytes(N)?.try_into().ok()
    }

    /// The length prefix of a Zcash vector: a canonical CompactSize of at most `MAX_COMPACT_SIZE`.
    fn vector_len(&mut self) -> Option<usize> {
        let n = read_compact_size(self.data, &mut self.pos)?;
        if n > MAX_COMPACT_SIZE {
            return None;
        }
        usize::try_from(n).ok()
    }

    fn action_without_auth(&mut self) -> Option<Action<()>> {
        let cv_net = Option::from(ValueCommitment::from_bytes(&self.array::<ELEMENT_LEN>()?))?;
        let nf = Option::from(Nullifier::from_bytes(&self.array::<ELEMENT_LEN>()?))?;
        let rk = VerificationKey::<SpendAuth>::try_from(self.array::<ELEMENT_LEN>()?).ok()?;
        let cmx = Option::from(ExtractedNoteCommitment::from_bytes(&self.array::<ELEMENT_LEN>()?))?;
        let encrypted_note = TransmittedNoteCiphertext {
            epk_bytes: self.array::<ELEMENT_LEN>()?,
            enc_ciphertext: self.array::<ENC_CIPHERTEXT_LEN>()?,
            out_ciphertext: self.array::<OUT_CIPHERTEXT_LEN>()?,
        };
        Action::from_parts(nf, rk, cmx, encrypted_note, cv_net, ()).ok()
    }

    fn signature<T: SigType>(&mut self) -> Option<Signature<T>> {
        Some(Signature::from(self.array::<SIGNATURE_LEN>()?))
    }
}

/// Reads one Ironwood bundle that fills `data` exactly. `None` for anything the replaced reader
/// refused: bad layout, a non-canonical element, an empty bundle, flags outside the Ironwood v3
/// grammar, a value balance out of range, a proof of non-canonical size, or bytes left over.
pub fn read_ironwood_bundle(data: &[u8]) -> Option<Bundle<Authorized, ZatBalance>> {
    // NU6.3, the branch the chain verifies under, selects protocol revision 3 for the Ironwood pool.
    let version = BundleVersion::ironwood_v3();
    let mut r = Reader { data, pos: 0 };

    let n = r.vector_len()?;
    let mut actions = Vec::new();
    for _ in 0..n {
        actions.push(r.action_without_auth()?);
    }
    if actions.is_empty() {
        return None;
    }
    let flags = Flags::from_byte(r.array::<1>()?[0], version)?;
    let value_balance = ZatBalance::from_i64_le_bytes(r.array::<VALUE_BALANCE_LEN>()?).ok()?;
    let anchor = Option::from(Anchor::from_bytes(r.array::<ELEMENT_LEN>()?))?;
    let proof_len = r.vector_len()?;
    let proof = Proof::new(r.bytes(proof_len)?.to_vec());
    let mut authorized = Vec::with_capacity(actions.len());
    for action in actions {
        let sig = r.signature::<SpendAuth>()?;
        authorized.push(action.map(|()| sig));
    }
    let binding = r.signature()?;
    if r.pos != data.len() {
        return None;
    }

    Bundle::try_from_parts(
        NonEmpty::from_vec(authorized)?,
        flags,
        value_balance,
        anchor,
        Authorized::from_parts(proof, binding),
        version,
    )
    .ok()
}
