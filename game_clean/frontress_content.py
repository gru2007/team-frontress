#!/usr/bin/env python3
"""Build/verify the checkout's resources as a VPK mounted before upstream pak1.

The repository deliberately omits upstream models/materials. Keep pak1 as the
asset dependency, but never use it as the authority for our tracked resources.
VPK v1, single file with inline data; no Valve SDK binary or signing key needed.
"""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import struct
import subprocess
import tempfile
import zlib

ROOT = Path(__file__).resolve().parents[1]
SOURCE = "game_src/tc2/pak1"
ARCHIVE = "frontress_dir.vpk"
MANIFEST = "frontress_content.json"
HEADER = struct.Struct("<III")
ENTRY = struct.Struct("<IHHIIH")
SIGNATURE = 0x55AA1234
INLINE = 0x7FFF


def git(*args):
    return subprocess.check_output(["git", "-C", str(ROOT), *args])


def source_files():
    files = {}
    for name in git("ls-files", "-z", "--", SOURCE).decode().split("\0"):
        if not name:
            continue
        path = Path(name)
        relative = path.relative_to(SOURCE).as_posix().lower()
        if relative in files:
            raise ValueError(f"Case-insensitive VPK path collision: {relative}")
        data = (ROOT / path).read_bytes()
        if data.startswith(b"version https://git-lfs.github.com/spec/v1"):
            raise ValueError(f"Unresolved Git LFS pointer: {name}")
        files[relative] = data
    if not files:
        raise ValueError("No tracked resource files in checkout")
    return files


def make_vpk(files):
    groups = {}
    for name, data in sorted(files.items()):
        path = PurePosixPath(name)
        extension = path.suffix[1:] or " "
        stem = path.name[:-len(path.suffix)] if path.suffix else path.name
        directory = str(path.parent) if str(path.parent) != "." else " "
        groups.setdefault(extension, {}).setdefault(directory, []).append((stem, data))
    tree, payload = bytearray(), bytearray()

    def string(value):
        tree.extend(value.encode("utf-8") + b"\0")

    for extension, directories in sorted(groups.items()):
        string(extension)
        for directory, entries in sorted(directories.items()):
            string(directory)
            for stem, data in entries:
                string(stem)
                tree.extend(ENTRY.pack(zlib.crc32(data), 0, INLINE, len(payload), len(data), 0xFFFF))
                payload.extend(data)
            string("")
        string("")
    string("")
    return HEADER.pack(SIGNATURE, 1, len(tree)) + tree + payload


def read_vpk(data):
    signature, version, size = HEADER.unpack_from(data)
    if signature != SIGNATURE or version != 1:
        raise ValueError("Expected Frontress VPK v1")
    end = HEADER.size + size
    if end > len(data):
        raise ValueError("Truncated VPK directory")
    cursor = HEADER.size

    def string():
        nonlocal cursor
        terminator = data.index(b"\0", cursor, end)
        value = data[cursor:terminator].decode("utf-8")
        cursor = terminator + 1
        return value

    files = {}
    while extension := string():
        while directory := string():
            while stem := string():
                if cursor + ENTRY.size > end:
                    raise ValueError("Truncated VPK entry")
                crc, preload, archive, offset, length, terminator = ENTRY.unpack_from(data, cursor)
                cursor += ENTRY.size
                if preload or archive != INLINE or terminator != 0xFFFF:
                    raise ValueError("Unexpected Frontress VPK entry layout")
                content = data[end + offset:end + offset + length]
                if len(content) != length or zlib.crc32(content) != crc:
                    raise ValueError("VPK content length/CRC mismatch")
                name = ("" if directory == " " else directory + "/") + stem
                name += "" if extension == " " else "." + extension
                if name in files:
                    raise ValueError(f"Duplicate VPK entry: {name}")
                files[name] = content
    if cursor != end:
        raise ValueError("Unexpected data in VPK directory")
    return files


def digest(data):
    return hashlib.sha256(data).hexdigest()


def build(destination):
    files = source_files()
    archive = make_vpk(files)
    manifest = {
        "commit": git("rev-parse", "HEAD").decode().strip(),
        "source": SOURCE,
        "archive_sha256": digest(archive),
        "files": {name: digest(data) for name, data in sorted(files.items())},
    }
    destination.mkdir(parents=True, exist_ok=True)
    # Replace only complete outputs. A previous workspace cache is never reused.
    for name, data in ((ARCHIVE, archive), (MANIFEST, (json.dumps(manifest, indent=2) + "\n").encode())):
        with tempfile.NamedTemporaryFile(dir=destination, delete=False) as temp:
            temp.write(data)
            temporary = Path(temp.name)
        temporary.replace(destination / name)
    verify(destination)


def verify(destination):
    archive = (destination / ARCHIVE).read_bytes()
    manifest = json.loads((destination / MANIFEST).read_text())
    if manifest["commit"] != git("rev-parse", "HEAD").decode().strip():
        raise ValueError("Resource package comes from a different commit")
    if digest(archive) != manifest["archive_sha256"]:
        raise ValueError("Resource package SHA256 mismatch")
    files = read_vpk(archive)
    if {name: digest(data) for name, data in files.items()} != manifest["files"]:
        raise ValueError("Resource manifest does not match VPK entries")
    sources = source_files()
    # Windows git checkouts may use CRLF. Compare text logically only when the
    # byte hashes differ (publish runs on Linux for both platform artifacts).
    if files.keys() != sources.keys():
        raise ValueError("Packaged resource list differs from this checkout")
    for name, expected in sources.items():
        actual = files[name]
        if actual == expected:
            continue
        if b"\0" not in expected and actual.replace(b"\r\n", b"\n") == expected.replace(b"\r\n", b"\n"):
            continue
        raise ValueError(f"Packaged resource is stale: {name}")
    for info in ("gameinfo.txt", "gameinfo_server.txt"):
        contents = (destination / info).read_text()
        overlay = "game+mod+vgui |gameinfo_path|frontress.vpk"
        base = "game+mod+vgui |gameinfo_path|pak1.vpk"
        if overlay not in contents or base not in contents or contents.index(overlay) > contents.index(base):
            raise ValueError(f"{info} must mount frontress.vpk before pak1.vpk")
    print(f"Verified {len(files)} checkout resources in {destination / ARCHIVE}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("build", "verify"))
    parser.add_argument("destination", type=Path, help="tc2 directory containing gameinfo files")
    args = parser.parse_args()
    try:
        (build if args.action == "build" else verify)(args.destination)
    except (OSError, ValueError, KeyError, struct.error) as error:
        parser.exit(1, f"Resource packaging failed: {error}\n")
