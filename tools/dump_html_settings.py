#!/usr/bin/env python3
"""Dump the TC2 HTML settings schema out of the built Astro bundle.

The settings UI the TC2 mod built lives in HTML/JS and its source is not in this
repository -- only the compiled bundle under
game/tc2/loose/resource/html/_astro/. The schema is plain JSON embedded in that
bundle, so it can be read back out, and that is what this does: it writes
docs/SETTINGS.md, which is the list of every setting and the convar behind it.

Run it after replacing the bundle:

    python3 tools/dump_html_settings.py
"""

import glob
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUNDLE_GLOB = os.path.join(ROOT, "game/tc2/loose/resource/html/_astro/SettingsView.*.js")
OUT = os.path.join(ROOT, "docs/SETTINGS.md")

HEADER = """# TC2 HTML settings, as data

Extracted from the built Astro bundle
(`game/tc2/loose/resource/html/_astro/SettingsView.*.js`), which is the only
copy of it in this repository -- the source project is elsewhere. This is the
list, not a design: {groups} groups, {count} settings, and what each one
actually drives.

It exists so the settings can be rebuilt somewhere they are reachable. The HTML
menu is off by default (`tf_main_menu_html 0`), so today none of this is on
screen; the stock VGUI options are, and they are a flat pile with none of these
groupings.

Regenerate with `tools/dump_html_settings.py`.
"""


def find_bundle():
    matches = sorted(glob.glob(BUNDLE_GLOB))
    if not matches:
        sys.exit("no SettingsView bundle found under %s" % BUNDLE_GLOB)
    return matches[-1]


def extract_groups(text):
    """Pull every {"name":..,"settings":[..]} object out by balancing brackets.

    A regex cannot do this: the settings arrays nest and contain strings with
    braces in them. Balancing while skipping string literals can.
    """
    groups = []
    for m in re.finditer(r'\{"name":"([^"]+)","settings":\[', text):
        start = m.start()
        depth = 0
        i = start
        while i < len(text):
            c = text[i]
            if c == '"':
                i += 1
                while i < len(text):
                    if text[i] == "\\":
                        i += 2
                        continue
                    if text[i] == '"':
                        break
                    i += 1
            elif c in "{[":
                depth += 1
            elif c in "}]":
                depth -= 1
                if depth == 0:
                    break
            i += 1
        try:
            groups.append(json.loads(text[start : i + 1]))
        except ValueError as err:
            print("skipping %s: %s" % (m.group(1), err), file=sys.stderr)
    return groups


def describe(setting):
    kind = setting.get("type")
    if kind == "slider":
        return " — slider %s..%s step %s" % (
            setting.get("min"),
            setting.get("max"),
            setting.get("step"),
        )
    if kind == "select":
        return " — one of: " + ", ".join(
            "`%s`" % o.get("value") for o in setting.get("options", [])
        )
    if kind == "check":
        return " — checkbox"
    if kind == "action":
        return " — button"
    return " — %s" % kind if kind else ""


def main():
    text = open(find_bundle(), encoding="utf-8", errors="replace").read()
    groups = extract_groups(text)
    count = sum(len(g.get("settings", [])) for g in groups)

    out = [HEADER.format(groups=len(groups), count=count)]
    for group in groups:
        out.append("\n## %s\n" % group.get("name"))
        for setting in group.get("settings", []):
            out.append(
                "- **%s** `%s`%s"
                % (setting.get("label", ""), setting.get("cvar", ""), describe(setting))
            )
            if setting.get("tooltip"):
                out.append("  - %s" % setting["tooltip"])

    open(OUT, "w").write("\n".join(out) + "\n")
    print("wrote %s: %d groups, %d settings" % (OUT, len(groups), count))


if __name__ == "__main__":
    main()
