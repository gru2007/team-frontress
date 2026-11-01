#!/bin/bash
# Builds bin/osxarm64/{protoc,libprotobuf.a} for the macOS build (arm64).
#
# The tree here is Valve's patched protobuf 2.6.1. descriptor.pb.cc and
# plugin.pb.cc are generated files that the repository's .gitignore drops, so
# pass a stock protobuf-2.6.1 checkout to take them from.
#
#   ./build_osx.sh /path/to/protobuf-2.6.1
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
stock="${1:?path to a stock protobuf-2.6.1 tree}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp -R "$here" "$work/pb"
cp "$stock/src/google/protobuf/descriptor.pb.cc" "$work/pb/src/google/protobuf/"
cp "$stock/src/google/protobuf/compiler/plugin.pb.cc" "$work/pb/src/google/protobuf/compiler/"
cd "$work/pb"
flags="-O2 -mmacosx-version-min=11.0 -arch arm64"
sh ./configure --build=aarch64-apple-darwin --host=aarch64-apple-darwin \
	--disable-shared --enable-static --without-zlib CC=clang CXX=clang++ \
	CFLAGS="$flags" CXXFLAGS="$flags -std=gnu++11 -Wno-everything"
make -j"$(sysctl -n hw.ncpu)"
mkdir -p "$here/bin/osxarm64"
cp src/protoc "$here/bin/osxarm64/protoc"
cp src/.libs/libprotobuf.a "$here/bin/osxarm64/libprotobuf.a"
