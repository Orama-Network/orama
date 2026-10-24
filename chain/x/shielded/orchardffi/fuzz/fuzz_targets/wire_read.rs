//! The in-house Ironwood bundle reader against the one it replaced.
//!
//! `orama_orchard::wire::read_ironwood_bundle` must refuse exactly what zcash_primitives 0.30.1
//! `read_v6_bundle(_, BranchId::Nu6_3, ValuePool::Ironwood)` refuses (counting bytes left over as a
//! refusal), and on what both accept build the same bundle: the same version and flags, and the
//! same encoding under zcash_primitives' writer. tests/wire.rs checks the same property on the
//! committed vectors and their mutations; this target searches beyond them.
#![no_main]

use std::io::Cursor;

use libfuzzer_sys::fuzz_target;
use orama_orchard::wire::read_ironwood_bundle;
use orchard::bundle::{Authorized, BundleVersion, Flags};
use orchard::{Bundle, ValuePool};
use zcash_primitives::transaction::components::orchard::{read_v6_bundle, write_v6_bundle};
use zcash_protocol::consensus::BranchId;
use zcash_protocol::value::ZatBalance;

fn built(bundle: &Bundle<Authorized, ZatBalance>) -> (BundleVersion, Flags, Vec<u8>) {
    let mut encoding = Vec::new();
    write_v6_bundle(Some(bundle), &mut encoding).expect("a bundle a reader built encodes");
    (bundle.bundle_version(), *bundle.flags(), encoding)
}

fuzz_target!(|data: &[u8]| {
    let mut cursor = Cursor::new(data);
    let reference = match read_v6_bundle(&mut cursor, BranchId::Nu6_3, ValuePool::Ironwood) {
        Ok(Some(bundle)) if cursor.position() as usize == data.len() => Some(built(&bundle)),
        _ => None,
    };
    let in_house = read_ironwood_bundle(data).map(|bundle| built(&bundle));
    assert_eq!(in_house, reference, "the readers differ on {} bytes", data.len());
});
