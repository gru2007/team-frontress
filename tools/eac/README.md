# Easy Anti-Cheat build tools

Easy Anti-Cheat (EOS Anti-Cheat) replaces VAC. The game code is in
`src/game/{shared,client,server}/tf/**/frontress_anticheat_*` and
`frontress_eos.*`; the operator guide is
[deploy/frontress/ANTICHEAT_EAC_RU.md](../../deploy/frontress/ANTICHEAT_EAC_RU.md).

| Script | What it does |
| --- | --- |
| `stage_eos_sdk.sh` | Lays the EOS SDK out in `src/thirdparty/eos_sdk` (never committed) and writes the product identity header the game is built with. |
| `package_eac.py` | Turns a packaged client (`game_dist`) into a protected one: EOS runtime, bootstrapper, `EasyAntiCheat/Settings.json`, Steam `installscript.vdf` (Windows), and a signed, verified integrity catalog. |

## CI

`.github/workflows/build.yml` runs both when `EOS_SDK_URL` is set; a `release/*`
tag fails without it (unless `vars.EAC_REQUIRED` is `false`).

| Kind | Name | Value |
| --- | --- | --- |
| secret | `EOS_SDK_URL` | URL of a zip of the EOS SDK folder (the one that contains `SDK/`), e.g. a private release asset `https://api.github.com/repos/<owner>/<repo>/releases/assets/<id>` |
| secret | `EOS_SDK_TOKEN` | Bearer token for that URL (optional) |
| secret | `EOS_CLIENT_SECRET` | GameClient client secret |
| secret | `EAC_INTEGRITY_PRIVATE_KEY_B64` | `base64 -i base_private.key` |
| secret | `EAC_INTEGRITY_PUBLIC_CERT_B64` | `base64 -i base_public.cer` |
| variable | `EOS_PRODUCT_ID` | defaults to `a626b6da0c3747dcb4481023976760d6` |
| variable | `EOS_SANDBOX_ID`, `EOS_DEPLOYMENT_ID` | from Product Settings |
| variable | `EOS_CLIENT_ID` | GameClient client ID |
| variable | `EOS_SDK_SHA256` | optional checksum of the zip |
| variable | `EAC_REQUIRED` | `false` lets a release build without EAC |

## Locally

```bash
export EOS_SDK_DIR=~/Downloads/EOS-SDK-58105819-Release-v1
export FRONTRESS_EOS_PRODUCT_ID=a626b6da0c3747dcb4481023976760d6 FRONTRESS_EOS_SANDBOX_ID=... \
       FRONTRESS_EOS_DEPLOYMENT_ID=... FRONTRESS_EOS_CLIENT_ID=... FRONTRESS_EOS_CLIENT_SECRET=...
./tools/eac/stage_eos_sdk.sh
VPC_EXTRA_ARGS=/define:FRONTRESS_EAC VPC_NINJA_EXTRA_SUFFIX=eac_ ./src/buildallprojects   # Linux
./game_clean/copy.sh
python3 tools/eac/package_eac.py --dist game_dist --platform linux \
    --key /path/to/base_private.key --cert /path/to/base_public.cer
```

macOS (private engine): `FRONTRESS_EAC=1 ./src/buildallprojects_osx`, then
`FRONTRESS_EAC=1 source-engine/scripts/frontress/package_macos.sh`, which calls
`package_eac.py --platform mac` (pass `--key/--cert` through
`FRONTRESS_EAC_PACKAGE_ARGS`). `macos.yml` does the same in CI.

Without `/define:FRONTRESS_EAC` the game builds as before; a protected server
then tells such a client why it cannot join. The integrity tool of the machine
that runs `package_eac.py` signs a client for any platform.

Never commit or ship `base_private.key` or a `<exe>.eac` module test config;
`.gitignore`, `package_eac.py` and `steampipe.sh` all keep them out.
