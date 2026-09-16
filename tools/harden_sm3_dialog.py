#!/usr/bin/env python3
"""One-time guarded hardening patch for the experimental SM3 diagnostic dialog."""
from pathlib import Path
path = Path('src/game/client/tf/frontress_sm3_compat.h')
text = path.read_text(encoding='utf-8')
old = '    swprintf_s( details, ARRAYSIZE( details ),\n'
new = '    _snwprintf_s( details, ARRAYSIZE( details ), _TRUNCATE,\n'
if new in text:
    print('Already hardened')
elif text.count(old) == 1:
    path.write_text(text.replace(old, new, 1), encoding='utf-8', newline='')
    print('Changed GPU details formatter to truncating form')
else:
    raise SystemExit('Unexpected shader compatibility header; refusing to edit')
