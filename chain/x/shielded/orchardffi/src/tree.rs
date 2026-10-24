//! The Orchard note-commitment tree frontier, as a stateless function over bytes.
//!
//! The chain stores the frontier (the right edge of the depth-32 Sinsemilla tree) and never the
//! whole tree. Sinsemilla hashing exists only in Rust, so appending commitments and reading the
//! root happen here; Go keeps the bytes.
//!
//! Frontier encoding (all little-endian): an empty tree is zero bytes. Otherwise
//! `position (u64) || leaf (32) || ommers (32 each)`; the ommer count is fixed by the position
//! (one per set bit), and a length that disagrees is refused.

use incrementalmerkletree::frontier::Frontier;
use incrementalmerkletree::{Hashable, Level, Position};
use orchard::tree::MerkleHashOrchard;

/// The depth of the Orchard note-commitment tree.
pub const DEPTH: u8 = orchard::NOTE_COMMITMENT_TREE_DEPTH as u8;

/// Bytes in a commitment, a tree node and a root.
pub const NODE_LEN: usize = 32;
/// Largest encoded frontier: position, leaf and one ommer per level.
pub const MAX_FRONTIER_LEN: usize = 8 + NODE_LEN + NODE_LEN * DEPTH as usize;

type Tree = Frontier<MerkleHashOrchard, DEPTH>;

fn node(bytes: &[u8]) -> Option<MerkleHashOrchard> {
    let raw: [u8; NODE_LEN] = bytes.try_into().ok()?;
    Option::from(MerkleHashOrchard::from_bytes(&raw))
}

/// Decodes a frontier, refusing anything that is not exactly what `encode` writes.
pub fn decode(data: &[u8]) -> Option<Tree> {
    if data.is_empty() {
        return Some(Tree::empty());
    }
    if data.len() < 8 + NODE_LEN {
        return None;
    }
    let position = u64::from_le_bytes(data[..8].try_into().ok()?);
    let leaf = node(&data[8..8 + NODE_LEN])?;
    let rest = &data[8 + NODE_LEN..];
    if rest.len() % NODE_LEN != 0 {
        return None;
    }
    let ommers = rest
        .as_chunks::<NODE_LEN>()
        .0
        .iter()
        .map(|ommer| node(ommer))
        .collect::<Option<Vec<_>>>()?;
    Tree::from_parts(Position::from(position), leaf, ommers).ok()
}

/// Encodes a frontier the way `decode` reads it.
pub fn encode(tree: &Tree) -> Vec<u8> {
    let Some(frontier) = tree.value() else {
        return Vec::new();
    };
    let mut out = Vec::with_capacity(MAX_FRONTIER_LEN);
    out.extend_from_slice(&u64::from(frontier.position()).to_le_bytes());
    out.extend_from_slice(&frontier.leaf().to_bytes());
    for ommer in frontier.ommers() {
        out.extend_from_slice(&ommer.to_bytes());
    }
    out
}

/// The root of a tree with no commitments.
pub fn empty_root() -> [u8; NODE_LEN] {
    MerkleHashOrchard::empty_root(Level::from(DEPTH)).to_bytes()
}

/// Appends each commitment in order. Returns the new frontier and its root, or `None` when the
/// frontier does not decode, a commitment is not a canonical field element, or the tree is full.
pub fn append(frontier: &[u8], commitments: &[u8]) -> Option<(Vec<u8>, [u8; NODE_LEN])> {
    if commitments.len() % NODE_LEN != 0 {
        return None;
    }
    let mut tree = decode(frontier)?;
    for cmx in commitments.as_chunks::<NODE_LEN>().0 {
        if !tree.append(node(cmx)?) {
            return None;
        }
    }
    Some((encode(&tree), tree.root().to_bytes()))
}

/// The height of a subtree whose root the chain reports when it fills: 2^16 leaves, the shard
/// height of the Orchard shard tree (zcash `SHARD_HEIGHT`).
pub const SUBTREE_LEVEL: u8 = 16;

type Subtree = Frontier<MerkleHashOrchard, SUBTREE_LEVEL>;

/// The root of the 2^16-leaf subtree whose last leaf is the last leaf of `frontier`. `None` when
/// the frontier does not decode, is empty, or its last leaf does not close a subtree (the low 16
/// bits of its position are not all ones). The subtree's levels 0..15 are read off the frontier's
/// lowest ommers, which are the left siblings on the path of that last leaf.
pub fn subtree_root(frontier: &[u8]) -> Option<[u8; NODE_LEN]> {
    let tree = decode(frontier)?;
    let edge = tree.value()?;
    let position = u64::from(edge.position());
    let mask = (1u64 << SUBTREE_LEVEL) - 1;
    if position & mask != mask {
        return None;
    }
    let ommers = edge.ommers()[..SUBTREE_LEVEL as usize].to_vec();
    let subtree = Subtree::from_parts(Position::from(mask), *edge.leaf(), ommers).ok()?;
    Some(subtree.root().to_bytes())
}
