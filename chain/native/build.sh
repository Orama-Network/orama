#!/usr/bin/env bash
# Build the static native library for linux/amd64 (musl, C parts through zig): libwasmvm and the
# Orchard verifier in one archive, from source. Nothing prebuilt is downloaded.
#
# Usage: build.sh [build|record|verify]
#   build   writes $OUT/libwasmvm_muslc.x86_64.a (both Rust halves) and $OUT/liborama_orchard.a (empty:
#           the Go cgo directives of both libraries name their own archive, and the symbols of both are
#           in the first), and prints the sha256. It writes nothing in the checkout outside $OUT and the
#           ignored vendor/: `orama maint build` runs it, and a release build must leave the tracked
#           tree as it found it.
#   record  builds, then records the sha256 in native/libwasmvm_muslc.x86_64.a.sha256
#   verify  builds, then fails unless the sha256 equals the recorded one
#
# The wasmvm sources are libwasmvm from the Go module cache at the version chain/go.mod pins. The
# hash is only stable for the same Rust toolchain and zig; the recorded file names both.
set -euo pipefail

mode="${1:-build}"
case "$mode" in
build | record | verify) ;;
*)
	echo "usage: build.sh [build|record|verify]" >&2
	exit 2
	;;
esac
here="$(cd "$(dirname "$0")" && pwd)"
chain="$(cd "$here/.." && pwd)"
target="x86_64-unknown-linux-musl"
# 1.98.1 is the newest stable rustc; the pinned wasmvm and the Orchard crates build with it.
# src/probestack.rs still supplies __rust_probestack, which compiler_builtins only exports mangled.
toolchain="${RUST_TOOLCHAIN:-1.98.1}"
zig="${ORAMA_ZIG:-zig}"
out="${OUT:-$chain/build/native}"

# `go list -m` reports no directory for a module missing from the module cache (a clean runner).
(cd "$chain" && go mod download github.com/CosmWasm/wasmvm/v3)
wasmvm_dir="$(cd "$chain" && go list -m -f '{{.Dir}}' github.com/CosmWasm/wasmvm/v3)"
wasmvm_ver="$(cd "$chain" && go list -m -f '{{.Version}}' github.com/CosmWasm/wasmvm/v3)"
[ -d "$wasmvm_dir/libwasmvm" ] || { echo "no libwasmvm in $wasmvm_dir" >&2; exit 1; }

rm -rf "$here/vendor"
mkdir -p "$here/vendor"
cp -R "$wasmvm_dir/libwasmvm" "$here/vendor/wasmvm"
chmod -R u+w "$here/vendor"
rm -rf "$here/vendor/wasmvm/artifacts"

if [ ! -f "$here/Cargo.lock" ]; then
	cp "$here/vendor/wasmvm/Cargo.lock" "$here/Cargo.lock"
	echo "seeded Cargo.lock from wasmvm $wasmvm_ver" >&2
fi

# The archive must not depend on where the checkout lives. Cargo hashes a path dependency's location
# into every crate's metadata (and so into symbol names), and paths leak into debug strings, so the
# crate and its path dependency are built from one fixed directory, with that directory, the cargo
# home and the toolchain remapped. The recorded hash then matches a build from any checkout on any
# machine with the same toolchain. One build at a time: the directory is fixed.
stage="${ORAMA_NATIVE_STAGE:-/tmp/orama-native-build}"
rm -rf "$stage"
mkdir -p "$stage/native/patches" "$stage/x/shielded"
cp -R "$here/Cargo.toml" "$here/Cargo.lock" "$here/src" "$here/vendor" "$stage/native/"
rsync -a --exclude target "$here/patches/cosmwasm-crypto" "$stage/native/patches/"
rsync -a --exclude target "$chain/x/shielded/orchardffi" "$stage/x/shielded/"
cargo_home="${CARGO_HOME:-$HOME/.cargo}"
sysroot="$(rustup run "$toolchain" rustc --print sysroot)"
remap="--remap-path-prefix=$stage=/orama --remap-path-prefix=$cargo_home=/cargo --remap-path-prefix=$sysroot=/rustc"
cmap="-ffile-prefix-map=$stage=/orama -ffile-prefix-map=$cargo_home=/cargo"
(
	cd "$stage/native"
	ORAMA_ZIG="$zig" ZIG_TARGET=x86_64-linux-musl \
		CC_x86_64_unknown_linux_musl="$chain/scripts/zigcc.sh" \
		AR_x86_64_unknown_linux_musl="$zig ar" \
		CFLAGS_x86_64_unknown_linux_musl="$cmap" \
		CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_RUSTFLAGS="$remap" \
		rustup run "$toolchain" cargo build --release --locked --target "$target"
)

mkdir -p "$out"
cp "$stage/native/target/$target/release/liborama_native.a" "$out/libwasmvm_muslc.x86_64.a"
rm -f "$out/liborama_orchard.a"
"$zig" ar rc "$out/liborama_orchard.a"

sum="$(shasum -a 256 "$out/libwasmvm_muslc.x86_64.a" | cut -d' ' -f1)"
line="$sum  libwasmvm_muslc.x86_64.a  wasmvm=$wasmvm_ver rustc=$(rustup run "$toolchain" rustc --version | cut -d' ' -f2) zig=$("$zig" version)"
record="$here/libwasmvm_muslc.x86_64.a.sha256"
case "$mode" in
verify)
	want="$(cut -d' ' -f1 "$record")"
	if [ "$sum" != "$want" ]; then
		echo "MISMATCH: built $sum, recorded $want" >&2
		exit 1
	fi
	echo "verified $sum"
	;;
record)
	echo "$line" >"$record"
	echo "$line"
	;;
build)
	echo "$line"
	;;
esac
