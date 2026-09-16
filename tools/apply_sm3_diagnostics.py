#!/usr/bin/env python3
"""Apply the deliberately small Team Frontress SM3 diagnostic integration.

Run from the repository root: python3 tools/apply_sm3_diagnostics.py
The script fails closed if upstream's original guard changes.
"""
from pathlib import Path

FILE = Path('src/game/client/tf/clientmode_tf.cpp')
INCLUDE_ANCHOR = '#include "clientmode_tf.h"\n'
INCLUDE_NEW = '#include "frontress_sm3_compat.h"\n'
GUARD_START = '\tif ( !g_pMaterialSystemHardwareConfig->SupportsShaderModel_3_0() )\n\t{\n\t\tError(\n'
GUARD_END = '\n\t}\n\n\tbool bMultiPlayer = false;'
REPLACEMENT = ('\tif ( !g_pMaterialSystemHardwareConfig->SupportsShaderModel_3_0() )\n'
               '\t{\n'
               '\t\t// SM3 shaders remain mandatory. Only allow an explicit diagnostic bypass\n'
               '\t\t// when the adapter itself reports DX 9.0c+ but the engine rejects it.\n'
               '\t\tFrontress_HandleMissingShaderModel3( materials, g_pMaterialSystemHardwareConfig );\n'
               '\t}\n\n\tbool bMultiPlayer = false;')


def patch(text: str) -> str:
    already = 'Frontress_HandleMissingShaderModel3( materials, g_pMaterialSystemHardwareConfig );' in text
    if already:
        if INCLUDE_NEW not in text:
            raise ValueError('partial integration: function is present but include is missing')
        return text
    if text.count(INCLUDE_ANCHOR) != 1 or INCLUDE_NEW in text:
        raise ValueError('unexpected clientmode_tf.cpp includes')
    if text.count(GUARD_START) != 1:
        raise ValueError('SM3 guard missing or duplicated; do not patch unknown upstream')
    start = text.index(GUARD_START)
    end = text.find(GUARD_END, start)
    if end < 0:
        raise ValueError('SM3 guard end missing')
    old = text[start:end]
    if 'Your graphics card falls below our official minimum specs.' not in old or 'Shader Model 3.0' not in old:
        raise ValueError('original SM3 error no longer matches')
    text = text[:start] + REPLACEMENT + text[end + len(GUARD_END):]
    return text.replace(INCLUDE_ANCHOR, INCLUDE_ANCHOR + INCLUDE_NEW, 1)


def main():
    if not FILE.is_file():
        raise SystemExit('Run this script from the repository root')
    before = FILE.read_text(encoding='utf-8')
    after = patch(before)
    if after != before:
        FILE.write_text(after, encoding='utf-8', newline='')
        print('Applied SM3 diagnostic integration to', FILE)
    else:
        print('SM3 diagnostics already integrated')


if __name__ == '__main__':
    main()
