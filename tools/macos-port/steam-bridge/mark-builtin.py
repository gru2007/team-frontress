#!/usr/bin/env python3
"""Stamp a PE file with Wine's builtin-DLL marker.

Wine will only hand a module its Unix library if it considers that module a
builtin, and what it looks at is a fixed string in the DOS stub.  winebuild
writes it; mingw-w64 does not.  Rather than take a dependency on a full Wine
build tree just for one 16-byte constant, the bridge links with mingw-w64 and
patches the marker in afterwards.

The bytes being overwritten are the 16-bit stub program, which exists only to
print "This program cannot be run in DOS mode" on a real DOS box.  Nothing in
the Wine or Windows loader path executes it.
"""

import struct
import sys

# Seventeen bytes, not sixteen: Wine compares sizeof("Wine builtin DLL"), which
# in C includes the terminating NUL. A stub that leaves the next byte non-zero
# -- as mingw-w64's does, with the rest of "This program cannot be run in DOS
# mode" sitting right there -- is rejected with "found in WINEDLLPATH but not a
# builtin, ignoring", and the DLL then fails to load at all.
SIGNATURE = b"Wine builtin DLL\0"
OFFSET = 0x40


def stamp(path):
    with open(path, "rb") as handle:
        data = bytearray(handle.read())

    if data[:2] != b"MZ":
        raise SystemExit("%s: not a PE image" % path)

    e_lfanew = struct.unpack_from("<I", data, 0x3C)[0]
    if e_lfanew < OFFSET + len(SIGNATURE):
        raise SystemExit(
            "%s: DOS stub is too small for the builtin marker (e_lfanew=0x%x)"
            % (path, e_lfanew))
    if data[e_lfanew:e_lfanew + 4] != b"PE\0\0":
        raise SystemExit("%s: no PE header at e_lfanew" % path)

    if data[OFFSET:OFFSET + len(SIGNATURE)] == SIGNATURE:
        print("%s: already marked as a Wine builtin" % path)
        return

    # The whole stub is cleared, not just the signature's own bytes, so the
    # result matches what winebuild produces rather than merely satisfying the
    # comparison Wine happens to make today.
    data[OFFSET:e_lfanew] = b"\0" * (e_lfanew - OFFSET)
    data[OFFSET:OFFSET + len(SIGNATURE)] = SIGNATURE

    with open(path, "wb") as handle:
        handle.write(data)
    print("%s: marked as a Wine builtin" % path)


def main():
    if len(sys.argv) < 2:
        raise SystemExit("usage: mark-builtin.py <file.dll> [...]")
    for path in sys.argv[1:]:
        stamp(path)


if __name__ == "__main__":
    main()
