#!/usr/bin/env python3
"""Add a non-Steam shortcut so the mod shows up in the Steam library.

Steam keeps these in userdata/<id>/config/shortcuts.vdf, a binary VDF. Steam
rewrites that file when it exits, so it must not be running while we edit it.

The file is backed up before every write, and an existing entry with the same
executable is updated in place rather than duplicated.
"""

from __future__ import annotations

import argparse
import shutil
import struct
import sys
import time
from pathlib import Path

# Binary VDF type markers.
T_MAP = 0x00
T_STR = 0x01
T_INT = 0x02
T_END = 0x08


def read_cstr(buf: bytes, i: int) -> tuple[str, int]:
    end = buf.index(b"\x00", i)
    return buf[i:end].decode("utf-8", "replace"), end + 1


def parse(buf: bytes, i: int = 0) -> tuple[dict, int]:
    """Parse one map body, returning it and the index just past its terminator."""
    out: dict = {}
    while i < len(buf):
        marker = buf[i]
        i += 1
        if marker == T_END:
            return out, i
        key, i = read_cstr(buf, i)
        if marker == T_MAP:
            out[key], i = parse(buf, i)
        elif marker == T_STR:
            out[key], i = read_cstr(buf, i)
        elif marker == T_INT:
            out[key] = struct.unpack_from("<i", buf, i)[0]
            i += 4
        else:
            raise ValueError(f"unknown VDF marker 0x{marker:02x} at offset {i - 1}")
    return out, i


def serialize(node: dict) -> bytes:
    parts = bytearray()
    for key, value in node.items():
        name = key.encode("utf-8") + b"\x00"
        if isinstance(value, dict):
            parts += bytes([T_MAP]) + name + serialize(value)
        elif isinstance(value, bool):
            parts += bytes([T_INT]) + name + struct.pack("<i", int(value))
        elif isinstance(value, int):
            parts += bytes([T_INT]) + name + struct.pack("<i", value)
        else:
            parts += bytes([T_STR]) + name + str(value).encode("utf-8") + b"\x00"
    parts += bytes([T_END])
    return bytes(parts)


def user_config_dirs(steam_root: Path) -> list[Path]:
    userdata = steam_root / "userdata"
    if not userdata.is_dir():
        return []
    # Skip 'anonymous' (id 0); it is not a real logged-in account.
    return sorted(
        d / "config"
        for d in userdata.iterdir()
        if d.is_dir() and d.name.isdigit() and d.name != "0"
    )


def make_entry(name: str, exe: str, startdir: str, tags: list[str]) -> dict:
    # Steam expects the exe and start dir quoted, which is also what it writes
    # itself when you add a shortcut through the UI.
    return {
        "appid": 0,  # Steam assigns one on load when this is 0
        "AppName": name,
        "Exe": f'"{exe}"',
        "StartDir": f'"{startdir}"',
        "icon": "",
        "ShortcutPath": "",
        "LaunchOptions": "",
        "IsHidden": 0,
        "AllowDesktopConfig": 1,
        "AllowOverlay": 1,
        "OpenVR": 0,
        "Devkit": 0,
        "DevkitGameID": "",
        "DevkitOverrideAppID": 0,
        "LastPlayTime": 0,
        "FlatpakAppID": "",
        "tags": {str(i): t for i, t in enumerate(tags)},
    }


def unique_backup_path(path: Path) -> Path:
    """A backup name that cannot collide, even on two runs in the same second."""
    stamp = time.strftime("%Y%m%d-%H%M%S")
    candidate = path.with_suffix(f".vdf.bak-{stamp}")
    counter = 1
    while candidate.exists():
        candidate = path.with_suffix(f".vdf.bak-{stamp}-{counter}")
        counter += 1
    return candidate


def install(config_dir: Path, entry: dict, exe_quoted: str) -> str:
    path = config_dir / "shortcuts.vdf"

    if path.exists():
        raw = path.read_bytes()
        try:
            root, _ = parse(raw)
        except ValueError as exc:
            return f"skipped {path}: cannot parse ({exc})"
        shortcuts = root.get("shortcuts", {})
        if not isinstance(shortcuts, dict):
            return f"skipped {path}: unexpected structure"
        backup = unique_backup_path(path)
        shutil.copy2(path, backup)
    else:
        root, shortcuts = {"shortcuts": {}}, {}
        root["shortcuts"] = shortcuts
        backup = None

    # Update in place if this executable is already listed, so re-running does
    # not fill the library with duplicates.
    for key, existing in shortcuts.items():
        if isinstance(existing, dict) and existing.get("Exe") == exe_quoted:
            preserved = existing.get("appid", 0)
            merged = dict(entry)
            merged["appid"] = preserved
            shortcuts[key] = merged
            path.write_bytes(serialize({"shortcuts": shortcuts}))
            return f"updated existing entry in {path}" + (f" (backup {backup.name})" if backup else "")

    next_index = str(max((int(k) for k in shortcuts if k.isdigit()), default=-1) + 1)
    shortcuts[next_index] = entry
    path.write_bytes(serialize({"shortcuts": shortcuts}))
    return f"added entry {next_index} to {path}" + (f" (backup {backup.name})" if backup else "")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--name", required=True)
    ap.add_argument("--exe", required=True)
    ap.add_argument("--startdir", required=True)
    ap.add_argument("--steam-root", required=True)
    ap.add_argument("--tag", action="append", default=["Greyline"])
    args = ap.parse_args()

    steam_root = Path(args.steam_root).expanduser()
    dirs = user_config_dirs(steam_root)
    if not dirs:
        print(f"no Steam user profiles under {steam_root}/userdata — log into Steam once first",
              file=sys.stderr)
        return 1

    exe = str(Path(args.exe).resolve())
    entry = make_entry(args.name, exe, str(Path(args.startdir).resolve()), args.tag)

    for config_dir in dirs:
        config_dir.mkdir(parents=True, exist_ok=True)
        print(install(config_dir, entry, f'"{exe}"'))

    print("restart Steam; the entry appears in the library once it reloads")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
