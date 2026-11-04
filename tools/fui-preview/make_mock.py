#!/usr/bin/env python3
"""Build mock.json for fui-preview from an installed Team Fortress 2.

The locker gets real items: names from tf_<lang>.txt, icons from the item
schema, so the previewer shows what the game will. Everything else (party,
war, career) is invented and only there to fill the screens.

    tools/fui-preview/make_mock.py [--tf2 DIR] [--lang russian] > tools/fui-preview/mock.json
"""
import argparse
import json
import os
import random
import re
import struct

CLASSES = ["scout", "soldier", "pyro", "demoman", "heavy", "engineer", "medic", "sniper", "spy"]
QUALITIES = ["unique"] * 6 + ["strange"] * 3 + ["vintage", "genuine", "unusual", "collectors", "haunted"]


def kv_tokens(text):
    for m in re.finditer(r'"((?:[^"\\]|\\.)*)"|([{}])|//[^\n]*|\[[^\]\n]*\]|([^\s{}"]+)', text):
        if m.group(1) is not None:
            yield m.group(1).replace('\\"', '"').replace("\\n", "\n")
        elif m.group(2):
            yield m.group(2)
        elif m.group(3):
            yield m.group(3)


def kv_parse(text):
    stack = [{}]
    key = None
    for t in kv_tokens(text):
        if t == "{":
            existing = stack[-1].get(key)
            d = existing if isinstance(existing, dict) else {}
            stack[-1][key] = d
            stack.append(d)
            key = None
        elif t == "}":
            if len(stack) > 1:
                stack.pop()
            key = None
        elif key is None:
            key = t
        else:
            stack[-1][key] = t
            key = None
    return stack[0]


def vpk_index(path):
    f = open(path, "rb")
    sig, ver, tree = struct.unpack("<III", f.read(12))
    hdr = 12 if ver == 1 else 28
    f.seek(hdr)
    data = f.read(tree)
    i = 0
    names = set()

    def cstr():
        nonlocal i
        j = data.index(b"\0", i)
        s = data[i:j].decode("latin1")
        i = j + 1
        return s

    while True:
        ext = cstr()
        if not ext:
            break
        while True:
            d = cstr()
            if not d:
                break
            while True:
                n = cstr()
                if not n:
                    break
                pre = struct.unpack_from("<H", data, i + 4)[0]
                i += 18 + pre
                names.add(((d + "/") if d != " " else "") + n + "." + ext)
    return names


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tf2", default=os.path.expanduser("~/Library/Application Support/Steam/steamapps/common/Team Fortress 2"))
    ap.add_argument("--lang", default="russian")
    args = ap.parse_args()
    tf = os.path.join(args.tf2, "tf")
    random.seed(7)

    schema = kv_parse(open(os.path.join(tf, "scripts/items/items_game.txt"), encoding="utf-8", errors="replace").read())["items_game"]
    lang = kv_parse(open(os.path.join(tf, "resource/tf_%s.txt" % args.lang), encoding="utf-16").read())
    tokens = {k.lower(): v for k, v in (lang.get("lang") or lang.get("Lang"))["Tokens"].items()}
    textures = vpk_index(os.path.join(tf, "tf2_textures_dir.vpk"))
    prefabs = schema["prefabs"]

    def resolve(item):
        out = {}
        for p in (item.get("prefab") or "").split():
            if p in prefabs:
                out.update(resolve(prefabs[p]))
        out.update(item)
        return out

    def loc(tok):
        if not tok:
            return ""
        return tokens.get(tok.lstrip("#").lower(), tok)

    def icon(path):
        if not path:
            return None
        path = path.lower()
        for cand in (path + "_large", path):
            if "materials/" + cand + ".vtf" in textures:
                return cand
        return None

    items = []
    seen = set()
    for defindex, raw in schema["items"].items():
        if not defindex.isdigit():
            continue
        it = resolve(raw)
        classes = [c for c in (it.get("used_by_classes") or {}) if c in CLASSES]
        slot = it.get("item_slot", "")
        if slot not in ("primary", "secondary", "melee", "pda", "pda2", "building", "misc", "head", "taunt", "action"):
            continue
        if slot in ("pda", "pda2", "building", "action"):
            continue
        if it.get("hidden") == "1" or it.get("baseitem") == "1" and int(defindex) > 30:
            continue
        name = loc(it.get("item_name"))
        img = icon(it.get("image_inventory"))
        if not name or not img or name.startswith("#") or (name, slot) in seen:
            continue
        if "festive" in it.get("name", "").lower() or "botkiller" in it.get("name", "").lower() or "Upgradeable" in it.get("name", ""):
            continue
        seen.add((name, slot))
        cosmetic = slot in ("misc", "head")
        quality = "unique" if it.get("baseitem") == "1" else random.choice(QUALITIES)
        items.append({
            "def": int(defindex),
            "name": name,
            "type": loc(it.get("item_type_name")),
            "icon": img,
            "slot": "cosmetic" if cosmetic else slot,
            "classes": classes if classes else CLASSES,
            "quality": quality,
            "stock": it.get("baseitem") == "1",
            "level": random.randint(1, 100) if not it.get("baseitem") == "1" else 1,
            "desc": loc(it.get("item_description"))[:220],
        })

    # A believable backpack: stock gear, up to 9 unlocks per weapon slot,
    # 14 cosmetics and 4 taunts per class.
    backpack = []
    for cls in CLASSES:
        for slot, cap in (("primary", 9), ("secondary", 9), ("melee", 9), ("cosmetic", 14), ("taunt", 4)):
            pool = [i for i in items if cls in i["classes"] and i["slot"] == slot and len(i["classes"]) <= 3]
            stock = [i for i in pool if i["stock"]]
            rest = [i for i in pool if not i["stock"]]
            random.shuffle(rest)
            for i in stock[:1] + rest[:cap]:
                if i not in backpack:
                    backpack.append(i)
    for n, i in enumerate(backpack):
        i["index"] = n
    mock = json.loads(open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "mock_base.json"), encoding="utf-8").read())
    mock.setdefault("locker", {})["items"] = backpack
    print(json.dumps(mock, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
