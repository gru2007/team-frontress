#!/usr/bin/env python3
"""Turn a packaged client (game_dist) into an Easy Anti-Cheat protected one.

Runs after game_clean/copy.sh (Windows, Linux) or package_macos.sh (macOS), on Windows, Linux or macOS (the integrity
tool for the machine it runs on signs a client for any platform). It:

  1. puts the EOS SDK runtime library next to the game modules;
  2. adds the anti-cheat bootstrapper and the EasyAntiCheat/ folder;
  3. fills in EasyAntiCheat/Settings.json (product, sandbox, deployment, which
     executable to protect);
  4. on Windows, writes installscript.vdf so Steam installs the EAC service;
  5. signs an integrity catalog of every file that holds code, with the
     product's integrity key, into EasyAntiCheat/Certificates/.

The integrity key never touches the repository or the output: it comes from
the environment (base64 of the files the EOS Developer Portal hands out) or a
local path, goes into a temporary copy of the tool's folder, and is deleted
with it.

Environment:
  FRONTRESS_EOS_PRODUCT_ID, FRONTRESS_EOS_SANDBOX_ID, FRONTRESS_EOS_DEPLOYMENT_ID
  EAC_INTEGRITY_PRIVATE_KEY_B64   base64 of base_private.key  (or --key FILE)
  EAC_INTEGRITY_PUBLIC_CERT_B64   base64 of base_public.cer   (or --cert FILE)
"""

import argparse
import base64
import json
import os
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, "..", ".."))

PLATFORMS = {
    "win": {
        "exe": "tc2_win64.exe",
        "runtime": ("EOSSDK-Win64-Shipping.dll", os.path.join("tc2", "bin", "x64")),
        "code_paths": ["tc2_win64.exe", "bin\\x64", "tc2\\bin\\x64"],
        # Our copies of the libraries TF2 ships unsigned: loaded instead of
        # TF2's, so they must be in the catalog (launcher_main PreloadOwnLibraries).
        "must_cover": ["SDL2.dll", "libfreetype-6.dll", "libfontconfig-1.dll",
                       "libharfbuzz-0.dll", "libxml2-2.dll"],
    },
    "linux": {
        "exe": "tc2_linux64",
        "runtime": ("libEOSSDK-Linux-Shipping.so", os.path.join("bin", "linux64")),
        "code_paths": ["tc2_linux64", "bin/linux64", "tc2/bin/linux64"],
    },
    # The macOS depot (source-engine scripts/frontress/package_macos.sh). Its
    # module folder depends on the build's architecture; "{plat}" is filled in
    # from the depot.
    "mac": {
        "exe": "tc2_osx",
        "runtime": ("libEOSSDK-Mac-Shipping.dylib", os.path.join("bin", "{plat}")),
        # Not bin/{plat} whole: its content/ holds shader caches and a sound
        # cache the game writes while it runs.
        "code_paths": ["tc2_osx", "bin/{plat}/*.dylib", "bin/{plat}/crashpad_handler", "tc2/bin/{plat}"],
    },
}

# What the bootstrapper is called in each depot, and which of the SDK's dist
# files to leave out (the x86-64-only macOS bootstrapper: ours is universal).
BOOTSTRAPPER = {
    "win": "start_protected_game.exe",
    "linux": "start_protected_game",
    "mac": "start_protected_game.app",
}
SKIP_DIST = {
    "mac": {"start_protected_game_x86-64.app"},
}

# The integrity tool runs where the build runs; the catalog it writes does not
# depend on the OS that signed it.
HOST_TOOLS = {
    "win32": ("win", "anticheat_integritytool.exe"),
    "linux": ("linux", "anticheat_integritytool"),
    "darwin": ("mac", "anticheat_integritytool"),
}

# What the integrity catalog must never cover: files the build rewrites after
# signing (retarget_appid.sh stamps steam.inf and gameinfo*.txt per Steam
# app) and files players are expected to change. Everything that holds code is
# listed in code_paths; these extensions are skipped inside those folders.
EXCLUDE_EXTENSIONS = [
    ".bak", ".bat", ".bmp", ".cfg", ".db", ".dbg", ".ico", ".inf", ".ini", ".jpg",
    ".json", ".last", ".log", ".manifest", ".mdb", ".pdb", ".png", ".pubkey", ".sh",
    ".txt", ".vdf", ".xml",
]

INSTALLSCRIPT = """"InstallScript"
{
\t"Run Process"
\t{
\t\t"EasyAntiCheat_Install"
\t\t{
\t\t\t"Process 1"\t\t"%%INSTALLDIR%%\\\\EasyAntiCheat\\\\EasyAntiCheat_EOS_Setup.exe"
\t\t\t"Command 1"\t\t"install %(product)s"
\t\t}
\t}
\t"Run Process On Uninstall"
\t{
\t\t"EasyAntiCheat_Uninstall"
\t\t{
\t\t\t"Process 1"\t\t"%%INSTALLDIR%%\\\\EasyAntiCheat\\\\EasyAntiCheat_EOS_Setup.exe"
\t\t\t"Command 1"\t\t"uninstall %(product)s"
\t\t}
\t}
}
"""


def fail(msg):
    print("error: " + msg, file=sys.stderr)
    sys.exit(1)


def env(name):
    value = os.environ.get(name, "").strip()
    if not value:
        fail(name + " is not set (see tools/eac/README.md)")
    return value


def mac_plat_dir(dist):
    for name in ("osxarm64", "osx64"):
        if os.path.isdir(os.path.join(dist, "bin", name)):
            return name
    fail("%s has neither bin/osxarm64 nor bin/osx64" % dist)


def integrity_config(platform, plat_dir=""):
    paths = ",\n        ".join(json.dumps(p.format(plat=plat_dir)) for p in PLATFORMS[platform]["code_paths"])
    exts = ",\n        ".join(json.dumps(e) for e in EXCLUDE_EXTENSIONS)
    return """# Generated by tools/eac/package_eac.py for %(platform)s.
config_info:
{
    version = 2;
};
search_options:
{
    exclude_size_threshold = 0;
    include_paths = [
        %(paths)s
    ];
    exclude_extensions = [
        %(exts)s
    ];
    ignore_files_without_extension = false;
};
runtime_configuration:
{
    max_active_game_instances = 1;
};
""" % {"platform": platform, "paths": paths, "exts": exts}


def read_secret(path_arg, env_name):
    if path_arg:
        with open(path_arg, "rb") as f:
            return f.read()
    value = env(env_name)
    try:
        return base64.b64decode(value, validate=False)
    except Exception as e:  # noqa: BLE001
        fail("%s is not base64: %s" % (env_name, e))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--dist", required=True, help="packaged client directory (game_dist)")
    ap.add_argument("--platform", required=True, choices=sorted(PLATFORMS))
    ap.add_argument("--sdk", default=os.path.join(ROOT, "src", "thirdparty", "eos_sdk"), help="staged SDK (stage_eos_sdk.sh)")
    ap.add_argument("--title", default="Team Frontress")
    ap.add_argument("--splash", default=os.path.join(ROOT, "game_clean", "eac", "SplashScreen.png"),
                    help="800x450 PNG for the bootstrapper; the SDK default is kept if missing")
    ap.add_argument("--key", help="base_private.key (default: $EAC_INTEGRITY_PRIVATE_KEY_B64)")
    ap.add_argument("--cert", help="base_public.cer (default: $EAC_INTEGRITY_PUBLIC_CERT_B64)")
    ap.add_argument("--no-sign", action="store_true", help="skip the integrity catalog (local tests only; protected servers will refuse the client)")
    args = ap.parse_args()

    plat = PLATFORMS[args.platform]
    dist = os.path.abspath(args.dist)
    sdk = os.path.abspath(args.sdk)
    tools = os.path.join(sdk, "tools", args.platform)

    if not os.path.isfile(os.path.join(dist, plat["exe"])):
        fail("%s has no %s; run game_clean/copy.sh first" % (dist, plat["exe"]))
    if not os.path.isdir(os.path.join(tools, "dist")):
        fail("%s is missing; run tools/eac/stage_eos_sdk.sh first" % tools)

    product = env("FRONTRESS_EOS_PRODUCT_ID")
    sandbox = env("FRONTRESS_EOS_SANDBOX_ID")
    deployment = env("FRONTRESS_EOS_DEPLOYMENT_ID")

    # A module test config (<exe>.eac) forces a client module onto whoever has
    # it. Epic: never ship one.
    for dirpath, _, files in os.walk(dist):
        for name in files:
            if name.endswith(".eac"):
                path = os.path.join(dirpath, name)
                print("removing anti-cheat test config " + os.path.relpath(path, dist))
                os.remove(path)

    # 1. EOS runtime, where the loader finds it for client and server modules.
    plat_dir = mac_plat_dir(dist) if args.platform == "mac" else ""
    runtime, runtime_dir = plat["runtime"]
    runtime_dir = runtime_dir.format(plat=plat_dir)
    src = os.path.join(sdk, "bin", runtime)
    if not os.path.isfile(src):
        fail("no %s in the staged SDK" % runtime)
    os.makedirs(os.path.join(dist, runtime_dir), exist_ok=True)
    shutil.copy2(src, os.path.join(dist, runtime_dir, runtime))

    # 2. Bootstrapper and EasyAntiCheat/.
    # symlinks=True keeps a macOS .app bundle (and Epic's signature over it)
    # exactly as shipped.
    skip = SKIP_DIST.get(args.platform, set())
    shutil.copytree(os.path.join(tools, "dist"), dist, dirs_exist_ok=True, symlinks=True,
                    ignore=lambda d, names: [n for n in names if n in skip])
    if args.platform == "linux":
        os.chmod(os.path.join(dist, "start_protected_game"), 0o755)

    # 3. Settings.json, keeping whatever keys this SDK's template has.
    settings_path = os.path.join(dist, "EasyAntiCheat", "Settings.json")
    with open(settings_path, encoding="utf-8") as f:
        settings = json.load(f)
    settings.update({
        "title": args.title,
        "executable": plat["exe"],
        "productid": product,
        "sandboxid": sandbox,
        "deploymentid": deployment,
        "requested_splash": "EasyAntiCheat/SplashScreen.png",
        # The bootstrapper stays until the game exits, so Steam keeps
        # tracking the launch (play time, "running" state) on every OS.
        "wait_for_game_process_exit": "true",
    })
    with open(settings_path, "w", encoding="utf-8") as f:
        json.dump(settings, f, indent="\t")
        f.write("\n")
    if os.path.isfile(args.splash):
        shutil.copy2(args.splash, os.path.join(dist, "EasyAntiCheat", "SplashScreen.png"))

    # 4. Steam runs this on install and uninstall (Windows only).
    if args.platform == "win":
        with open(os.path.join(dist, "installscript.vdf"), "w", encoding="utf-8", newline="\r\n") as f:
            f.write(INSTALLSCRIPT % {"product": product})

    # 5. The integrity catalog.
    if args.no_sign:
        print("warning: --no-sign: no integrity catalog; protected servers will refuse this client", file=sys.stderr)
        return

    key = read_secret(args.key, "EAC_INTEGRITY_PRIVATE_KEY_B64")
    cert = read_secret(args.cert, "EAC_INTEGRITY_PUBLIC_CERT_B64")
    host = HOST_TOOLS.get(sys.platform)
    if not host:
        fail("no integrity tool for " + sys.platform)
    exe = os.path.join(sdk, "tools", host[0], "devtools", host[1])
    if not os.path.isfile(exe):
        fail("%s is missing; run tools/eac/stage_eos_sdk.sh first" % exe)
    if host[0] != "win":
        os.chmod(exe, 0o755)
    workdir = tempfile.mkdtemp(prefix="eac-integrity-")
    try:
        cfg = os.path.join(workdir, "anticheat_integritytool.cfg")
        with open(cfg, "w", encoding="utf-8") as f:
            f.write(integrity_config(args.platform, plat_dir))
        paths = {}
        for name, data in (("base_private.key", key), ("base_public.cer", cert)):
            paths[name] = os.path.join(workdir, name)
            with open(paths[name], "wb") as f:
                f.write(data)
            os.chmod(paths[name], 0o600)
        print("signing the integrity catalog for %s" % dist)
        result = subprocess.run([exe, "-productid", product,
                                 "-inkey", paths["base_private.key"], "-incert", paths["base_public.cer"],
                                 "-target_game_dir", dist, cfg], cwd=workdir)
        if result.returncode != 0:
            fail("anticheat_integritytool exited with %d" % result.returncode)
    finally:
        shutil.rmtree(workdir, ignore_errors=True)

    certs = os.path.join(dist, "EasyAntiCheat", "Certificates")
    produced = sorted(os.listdir(certs)) if os.path.isdir(certs) else []
    if "base.bin" not in produced:
        fail("the integrity tool produced no EasyAntiCheat/Certificates/base.bin")
    print("integrity catalog: " + ", ".join(produced))

    # What a player's machine will check, checked here first: the catalog
    # covers the main executable and every listed file matches.
    listing = subprocess.run([exe, "-productid", product, "list", os.path.join(certs, "base.bin")],
                             capture_output=True, text=True)
    listed = listing.stdout + listing.stderr
    for required in [plat["exe"]] + plat.get("must_cover", []):
        if required not in listed:
            fail("the integrity catalog does not cover %s" % required)
    verify = subprocess.run([exe, "-productid", product, "verify", dist])
    if verify.returncode != 0:
        fail("the signed client does not verify (anticheat_integritytool verify exited with %d)" % verify.returncode)
    print("Easy Anti-Cheat client ready: %s (start through %s)" % (dist, BOOTSTRAPPER[args.platform]))


if __name__ == "__main__":
    main()
