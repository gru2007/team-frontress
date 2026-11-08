#!/usr/bin/env python3
"""List the Windows modules in a Team Fortress 2 bin folder that carry no
Authenticode signature, with their SHA-256 and size.

The Windows client runs TF2's engine, so Easy Anti-Cheat sees TF2's DLLs loaded
from TF2's folder. Valve signs nearly all of them; the few it does not (SDL2,
freetype, fontconfig, harfbuzz, libxml2, dxvk) are what the anti-cheat reports
as suspicious. Our integrity catalog cannot cover them (the integrity tool only
takes paths inside the game's own folder), so the same files ship in our
bin/x64 and load first (launcher_main PreloadOwnLibraries). Re-run this after
every TF2 update: when a hash differs from game/bin/x64, replace our copy.

    python3 tools/eac/tf2_unsigned_modules.py "<TF2>/bin/x64" [more folders...]
"""

import hashlib
import os
import struct
import sys

SECURITY_DIRECTORY = 4  # IMAGE_DIRECTORY_ENTRY_SECURITY


def is_signed(data):
    if data[:2] != b"MZ":
        return None
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    if data[pe:pe + 4] != b"PE\0\0":
        return None
    magic = struct.unpack_from("<H", data, pe + 24)[0]
    directories = pe + 24 + (112 if magic == 0x20B else 96)
    _, size = struct.unpack_from("<II", data, directories + 8 * SECURITY_DIRECTORY)
    return size > 0


def main():
    if len(sys.argv) < 2:
        print(__doc__.strip(), file=sys.stderr)
        sys.exit(2)
    print("| File | Size | SHA-256 |")
    print("| --- | ---: | --- |")
    for folder in sys.argv[1:]:
        for name in sorted(os.listdir(folder), key=str.lower):
            if not name.lower().endswith((".dll", ".exe")):
                continue
            with open(os.path.join(folder, name), "rb") as f:
                data = f.read()
            if is_signed(data) is False:
                print("| %s | %d | `%s` |" % (name, len(data), hashlib.sha256(data).hexdigest()))


if __name__ == "__main__":
    main()
