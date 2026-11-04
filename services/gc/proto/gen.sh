#!/bin/sh
# Regenerates internal/gcpb from the game's own .proto files.
#
# The game and the GC must agree on every message byte for byte, so there is
# exactly one copy of the protocol: the files the game is built from. This
# script reads them in place and never copies them.
#
# Needs protoc and protoc-gen-go (go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6).
set -eu
cd "$(dirname "$0")/.."
SRC=../../src
PKG=github.com/gru2007/team-frontress/services/gc/internal/gcpb
OUT=internal/gcpb
FILES="steammessages.proto gcsdk_gcmessages.proto gcsystemmsgs.proto base_gcmessages.proto tf_gcmessages.proto"

MAPS=""
for f in $FILES; do
	MAPS="$MAPS --go_opt=M$f=$PKG;gcpb"
done

rm -f "$OUT"/*.pb.go
# shellcheck disable=SC2086
protoc \
	-I "$SRC/gcsdk" \
	-I "$SRC/game/shared" \
	-I "$SRC/game/shared/tf" \
	--go_out="$OUT" --go_opt=paths=source_relative $MAPS \
	$FILES
