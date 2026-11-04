#!/usr/bin/env python3
"""Generate a ninja build for macOS from the project's .vpc scripts.

The VPC binary in devtools/bin only runs on Linux and Windows. This is a small
interpreter for the subset of VPC that the game projects use, so the macOS
build compiles exactly the files the official Linux/Windows builds do.

    devtools/vpc_osx.py [--debug] [--arch arm64] [+group|project ...]

It writes _vpc_/osx/<mode>/build.ninja; run ninja on that file. Engine-side
libraries (tier0, vstdlib, particles, tier2, ...) are not built here. They come
from the engine and are looked up in lib/public/osx64 (see README_MACOS.md).
"""

import argparse
import os
import re
import shlex
import subprocess
import sys

SRC = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


# ---------------------------------------------------------------------------
# Tokenizer
# ---------------------------------------------------------------------------

class Tok:
    __slots__ = ("kind", "val", "line", "file")

    def __init__(self, kind, val, line, file):
        self.kind, self.val, self.line, self.file = kind, val, line, file

    def __repr__(self):
        return "%s:%d %s %r" % (self.file, self.line, self.kind, self.val)


def tokenize(path):
    with open(path, "r", encoding="utf-8-sig", errors="replace") as f:
        text = f.read()
    toks = []
    i, n, line = 0, len(text), 1
    while i < n:
        c = text[i]
        if c == "\n":
            line += 1
            toks.append(Tok("nl", None, line - 1, path))
            i += 1
        elif c in " \t\r":
            i += 1
        elif c == "\\" and text[i + 1:i + 2] in ("\n", "\r") or (c == "\\" and text[i + 1:i + 3] == "\r\n"):
            # Line continuation: swallow the newline.
            i += 1
            while i < n and text[i] in "\r":
                i += 1
            if i < n and text[i] == "\n":
                i += 1
                line += 1
        elif text.startswith("//", i):
            while i < n and text[i] != "\n":
                i += 1
        elif text.startswith("/*", i):
            j = text.find("*/", i + 2)
            j = n if j < 0 else j + 2
            line += text.count("\n", i, j)
            i = j
        elif c == '"':
            j = i + 1
            buf = []
            while j < n and text[j] != '"':
                if text[j] == "\n":
                    line += 1
                buf.append(text[j])
                j += 1
            toks.append(Tok("str", "".join(buf), line, path))
            i = j + 1
        elif c in "{}":
            toks.append(Tok(c, c, line, path))
            i += 1
        elif c == "[":
            j = text.find("]", i)
            toks.append(Tok("cond", text[i + 1:j], line, path))
            i = j + 1
        else:
            j = i
            while j < n and text[j] not in " \t\r\n{}[\"" and not text.startswith("//", j):
                j += 1
            word = text[i:j]
            kind = "kw" if word.startswith("$") or word.startswith("-$") else "str"
            toks.append(Tok(kind, word, line, path))
            i = j
    toks.append(Tok("nl", None, line, path))
    return toks


class Stream:
    def __init__(self, toks):
        self.t = toks
        self.i = 0

    def peek(self, skip_nl=True):
        j = self.i
        while skip_nl and j < len(self.t) and self.t[j].kind == "nl":
            j += 1
        return self.t[j] if j < len(self.t) else None

    def next(self, skip_nl=True):
        while skip_nl and self.i < len(self.t) and self.t[self.i].kind == "nl":
            self.i += 1
        if self.i >= len(self.t):
            return None
        tok = self.t[self.i]
        self.i += 1
        return tok

    def args(self):
        """Strings up to end of line (or a block start), plus an optional condition."""
        vals, cond = [], None
        while self.i < len(self.t):
            tok = self.t[self.i]
            # Anything up to the end of the line is an argument, including
            # unquoted macro references such as $SRCDIR\lib\foo.
            if tok.kind == "nl" or tok.kind in "{}":
                break
            self.i += 1
            if tok.kind == "cond":
                cond = tok.val
            else:
                vals.append(tok.val)
        return vals, cond

    def at_block(self):
        tok = self.peek()
        return tok is not None and tok.kind == "{"

    def skip_block(self):
        assert self.next().kind == "{"
        depth = 1
        while depth:
            tok = self.next()
            if tok is None:
                return
            if tok.kind == "{":
                depth += 1
            elif tok.kind == "}":
                depth -= 1


# ---------------------------------------------------------------------------
# Interpreter
# ---------------------------------------------------------------------------

class ToolCfg(dict):
    """Configuration keys of one tool block ($Compiler, $Linker, ...)."""


class FileEntry:
    def __init__(self, path):
        self.path = path
        self.excluded = False
        self.compiler = {}
        self.custom = {}


class Project:
    def __init__(self, name):
        self.name = name
        self.files = {}        # normalized path -> FileEntry (ordered)
        self.libs = []         # (kind, path)
        self.tools = {}        # tool -> {key: value}


MACRO_RE = re.compile(r"\$([A-Za-z_][A-Za-z0-9_]*)")


class Interp:
    def __init__(self, conditionals, macros, config):
        self.conds = {k.upper(): v for k, v in conditionals.items()}
        self.macros = {k.upper(): v for k, v in macros.items()}
        self.config = config          # "Release" / "Debug"
        self.project = None
        self.base_dir = None
        self.custom_steps = {}     # extension -> $CustomBuildStep keys

    # -- macros/conditions ---------------------------------------------------

    def expand(self, s):
        for _ in range(8):
            def rep(m):
                name = m.group(1).upper()
                # Longest defined prefix wins, so $_IMPLIB_PREFIXvstdlib works.
                for k in range(len(name), 0, -1):
                    if name[:k] in self.macros:
                        return self.macros[name[:k]] + m.group(1)[k:]
                return m.group(0)
            new = MACRO_RE.sub(rep, s)
            if new == s:
                break
            s = new
        return s

    def cond(self, c):
        if not c:
            return True
        expr = c
        expr = expr.replace("&&", " and ").replace("||", " or ")
        expr = re.sub(r"!(?!=)", " not ", expr)

        def rep(m):
            v = self.conds.get(m.group(1).upper())
            if v is None:
                v = self.macros.get(m.group(1).upper())
            truthy = v not in (None, "", "0", 0, False)
            return " True " if truthy else " False "
        expr = MACRO_RE.sub(rep, expr)
        try:
            return bool(eval(expr, {"__builtins__": {}}, {}))
        except Exception:
            raise SystemExit("bad condition [%s]" % c)

    def path(self, p):
        p = self.expand(p).replace("\\", "/")
        if not os.path.isabs(p):
            p = os.path.join(self.base_dir, p)
        return os.path.normpath(p)

    # -- top level -------------------------------------------------------------

    def run_script(self, path):
        self.base_dir = os.path.dirname(os.path.abspath(path))
        self.macros.setdefault("ROOTSCRIPT", os.path.abspath(path))
        self.include(path)

    def include(self, path):
        if not os.path.exists(path):
            # VPC matches paths case-insensitively on Windows.
            d, b = os.path.split(path)
            if os.path.isdir(d):
                for f in os.listdir(d):
                    if f.lower() == b.lower():
                        path = os.path.join(d, f)
                        break
        if not os.path.exists(path):
            raise SystemExit("missing include %s" % path)
        self.statements(Stream(tokenize(path)), None)

    def statements(self, st, ctx):
        while True:
            tok = st.next()
            if tok is None:
                return
            if tok.kind == "}":
                return
            if tok.kind != "kw":
                if tok.kind == "{":
                    st.i -= 1
                    st.skip_block()
                continue
            self.statement(tok, st, ctx)

    def statement(self, tok, st, ctx):
        kw = tok.val.lower()
        if kw in ("$macro", "$macrorequired", "$macrorequiredallowempty"):
            vals, c = st.args()
            if not self.cond(c):
                return
            name = vals[0].lstrip("$").upper()
            val = self.expand(" ".join(vals[1:])) if len(vals) > 1 else ""
            if kw == "$macro" or name not in self.macros:
                if kw != "$macro" and len(vals) < 2 and name in self.macros:
                    return
                self.macros[name] = val
        elif kw == "$conditional":
            vals, c = st.args()
            if self.cond(c):
                self.conds[vals[0].lstrip("$").upper()] = self.expand(vals[1]) if len(vals) > 1 else "1"
        elif kw == "$include":
            vals, c = st.args()
            if self.cond(c):
                self.include(self.path(vals[0]))
        elif kw == "$configuration":
            vals, c = st.args()
            ok = self.cond(c) and (not vals or vals[0].lower() == self.config.lower())
            if st.at_block():
                if ok:
                    st.next()
                    self.config_block(st, ctx)
                else:
                    st.skip_block()
        elif kw == "$project":
            vals, c = st.args()
            ok = self.cond(c)
            if vals and ok:
                if self.project is None:
                    self.project = Project(self.expand(vals[0]))
            if st.at_block():
                if ok:
                    st.next()
                    self.project_block(st)
                else:
                    st.skip_block()
        elif kw == "$custombuildstep":
            vals, c = st.args()
            if st.at_block():
                if self.cond(c) and vals:
                    st.next()
                    step = {}
                    self.tool_block(st, step)
                    for ext in vals[0].split(";"):
                        self.custom_steps[ext.strip().lower().lstrip(".")] = step
                else:
                    st.skip_block()
        elif kw in ("$ignoreredundancywarning", "$loadaddressmacro", "$loadaddressmacroauto",
                    "$games", "$group"):
            st.args()
            if st.at_block():
                st.skip_block()
        else:
            st.args()
            if st.at_block():
                st.skip_block()

    # -- configuration -----------------------------------------------------------

    def config_block(self, st, file_entry):
        while True:
            tok = st.next()
            if tok is None or tok.kind == "}":
                return
            if tok.kind != "kw":
                continue
            tool = tok.val.lower().lstrip("$")
            vals, c = st.args()
            if not st.at_block():
                continue
            if not self.cond(c):
                st.skip_block()
                continue
            st.next()
            if file_entry is not None:
                target = file_entry.custom if tool == "custombuildstep" else file_entry.compiler
                if tool not in ("compiler", "custombuildstep"):
                    target = {}
            else:
                target = self.project_tools().setdefault(tool, {})
            self.tool_block(st, target)

    def project_tools(self):
        if self.project is None:
            self.project = Project(self.macros.get("PROJECTNAME", "project"))
        return self.project.tools

    def tool_block(self, st, target):
        while True:
            tok = st.next()
            if tok is None or tok.kind == "}":
                return
            if tok.kind != "kw":
                continue
            key = tok.val.lower().lstrip("$")
            vals, c = st.args()
            if st.at_block():
                st.skip_block()
                continue
            if not self.cond(c):
                continue
            val = "".join(vals) if key == "commandline" else " ".join(vals)
            old = target.get(key, "")
            val = val.replace("$BASE", old).replace("$base", old)
            target[key] = self.expand(val)

    # -- project -------------------------------------------------------------------

    def project_block(self, st):
        self.project_tools()
        while True:
            tok = st.next()
            if tok is None or tok.kind == "}":
                return
            if tok.kind != "kw":
                if tok.kind == "{":
                    st.i -= 1
                    st.skip_block()
                continue
            kw = tok.val.lower()
            vals, c = st.args()
            ok = self.cond(c)
            if kw == "$folder":
                if st.at_block():
                    if ok:
                        st.next()
                        self.project_block(st)
                    else:
                        st.skip_block()
            elif kw in ("$file", "$dynamicfile", "$shaders"):
                entries = []
                if ok and kw != "$shaders":
                    for v in vals:
                        p = self.path(v)
                        e = self.project.files.get(p)
                        if e is None:
                            e = self.project.files[p] = FileEntry(p)
                        entries.append(e)
                if st.at_block():
                    if ok and entries:
                        st.next()
                        self.file_block(st, entries)
                    else:
                        st.skip_block()
            elif kw == "-$file":
                if ok:
                    for v in vals:
                        self.project.files.pop(self.path(v), None)
                if st.at_block():
                    st.skip_block()
            elif kw in ("$lib", "$implib", "$libexternal", "$implibexternal"):
                if ok:
                    for v in vals:
                        v = self.expand(v).replace("\\", "/")
                        # A bare name is in $LIBPUBLIC, as in VPC.
                        if kw in ("$lib", "$implib") and "/" not in v:
                            v = self.macros.get("LIBPUBLIC", "") + "/" + v
                        self.project.libs.append((kw.lstrip("$"), v))
            elif kw in ("-$lib", "-$implib"):
                if ok:
                    for v in vals:
                        v = self.expand(v).replace("\\", "/")
                        self.project.libs = [l for l in self.project.libs
                                             if os.path.basename(l[1]) != os.path.basename(v)]
            else:
                if st.at_block():
                    st.skip_block()

    def file_block(self, st, entries):
        while True:
            tok = st.next()
            if tok is None or tok.kind == "}":
                return
            if tok.kind == "kw" and tok.val.lower() == "$configuration":
                vals, c = st.args()
                ok = self.cond(c) and (not vals or vals[0].lower() == self.config.lower())
                if st.at_block():
                    if ok:
                        st.next()
                        probe = FileEntry(None)
                        self.file_config(st, probe)
                        for e in entries:
                            e.compiler.update(probe.compiler)
                            e.custom.update(probe.custom)
                            e.excluded = e.excluded or probe.excluded
                    else:
                        st.skip_block()
            elif tok.kind == "{":
                st.i -= 1
                st.skip_block()

    def file_config(self, st, entry):
        while True:
            tok = st.next()
            if tok is None or tok.kind == "}":
                return
            if tok.kind != "kw":
                continue
            tool = tok.val.lower().lstrip("$")
            vals, c = st.args()
            if st.at_block():
                if not self.cond(c):
                    st.skip_block()
                    continue
                st.next()
                target = {}
                self.tool_block(st, target)
                if tool == "compiler":
                    entry.compiler.update(target)
                elif tool == "custombuildstep":
                    entry.custom.update(target)
                if target.get("excludedfrombuild", "").lower() == "yes":
                    entry.excluded = True
            elif tool == "excludedfrombuild" and self.cond(c):
                if vals and vals[0].lower() == "yes":
                    entry.excluded = True


# ---------------------------------------------------------------------------
# Groups / projects
# ---------------------------------------------------------------------------

def read_groups(interp):
    """Parse vpc_scripts/groups.vgc and projects.vgc."""
    groups, projects = {}, {}
    for name in ("groups.vgc", "projects.vgc"):
        st = Stream(tokenize(os.path.join(SRC, "vpc_scripts", name)))
        while True:
            tok = st.next()
            if tok is None:
                break
            if tok.kind != "kw":
                continue
            kw = tok.val.lower()
            vals, c = st.args()
            if not st.at_block():
                continue
            st.next()
            items = []
            while True:
                t = st.next()
                if t is None or t.kind == "}":
                    break
                if t.kind == "str":
                    items.append([t.val, None])
                elif t.kind == "cond" and items:
                    items[-1][1] = t.val
            items = [i for i, cc in items if interp.cond(cc)]
            if kw == "$group":
                groups[vals[0].lower()] = items
            elif kw == "$project":
                projects[vals[0].lower()] = [i.replace("\\", "/") for i in items]
    return groups, projects


# ---------------------------------------------------------------------------
# Ninja generation
# ---------------------------------------------------------------------------

SOURCE_EXT = {".cpp": "cxx", ".cc": "cxx", ".cxx": "cxx", ".c": "cc", ".mm": "objcxx", ".m": "objc"}


def ninja_escape(s):
    return s.replace("$", "$$").replace(" ", "$ ").replace(":", "$:")


def rel(p):
    return os.path.relpath(p, SRC)


def split_list(s):
    return [x.strip() for x in re.split(r"[;,]", s or "") if x.strip()]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("targets", nargs="*", default=["+game"])
    ap.add_argument("--debug", action="store_true")
    ap.add_argument("--arch", default=os.uname().machine)
    ap.add_argument("--dedicated", action="store_true")
    ap.add_argument("--out", default=None)
    ap.add_argument("--define", action="append", default=[])
    args = ap.parse_args()

    config = "Debug" if args.debug else "Release"
    mode = config.lower() + ("_dedicated" if args.dedicated else "")
    out_dir = args.out or os.path.join(SRC, "_vpc_", "osx", "%s_%s" % (mode, args.arch))
    os.makedirs(out_dir, exist_ok=True)

    # Matches PLATFORM_DIR in public/tier0/platform.h.
    platdir = "osxarm64" if args.arch == "arm64" else "osx64"
    conds = {
        "POSIX": 1, "OSXALL": 1, "OSX64": 1, "TF": 1, "SOURCESDK": 1, "GL": 1,
        "OSXARM64": 1 if args.arch == "arm64" else 0,
        "DEDICATED": 1 if args.dedicated else 0,
        # Replay is a dead feature and needs engine interfaces this engine does not
        # have; the macOS build leaves it out (REPLAY_ENABLED undefined).
        "BUILD_REPLAY": 0,
    }
    for d in args.define:
        conds[d] = 1
    macros = {
        "_DLL_EXT": ".dylib", "_STATICLIB_EXT": ".a", "_IMPLIB_EXT": ".dylib",
        "_IMPLIB_PREFIX": "lib", "_EXE_EXT": "", "PLATFORM": platdir,
        "PLATSUBDIR": "/" + platdir, "QUOTE": '"', "_SUBDIRSUFFIX": "", "_STATICSUBDIR": "",
        "_UNITYSUBDIR": "", "INTERMEDIATESUBDIR": "",
    }

    base_interp = Interp(conds, macros, config)
    groups, projects = read_groups(base_interp)

    # Prebuilt on Linux/Windows (lib/common), built from source here.
    extra_projects = {
        "libspng": ["thirdparty/libspng/libspng.vpc"],
        "frontress_effects": ["materialsystem/stdshaders/game_shader_frontress_effects_tf.vpc"],
    }
    projects.update(extra_projects)
    if "game" in groups:
        groups["game"] = list(extra_projects) + groups["game"]

    wanted = []
    for t in args.targets:
        if t.startswith("+"):
            wanted += groups[t[1:].lower()]
        else:
            wanted.append(t)

    cc = os.environ.get("CC", "clang")
    cxx = os.environ.get("CXX", "clang++")
    sdk = subprocess.run(["xcrun", "--show-sdk-path"], capture_output=True, text=True).stdout.strip()
    min_os = os.environ.get("MACOSX_DEPLOYMENT_TARGET", "11.0")
    opt = "-O0 -g" if args.debug else "-O2 -g -fno-strict-aliasing -ffast-math -fno-omit-frame-pointer"
    common = ("-arch %s -isysroot %s -mmacosx-version-min=%s -fPIC -fvisibility=hidden "
              "-fdiagnostics-color=always -Wno-everything -Werror=return-type "
              "-fno-delete-null-pointer-checks -DVPROF_LEVEL=1 -D_FILE_OFFSET_BITS=64 "
              "%s") % (args.arch, sdk, min_os, opt)
    if args.arch == "arm64":
        common += " -DPLATFORM_ARM64=1"
    ccache = "ccache " if subprocess.run(["which", "ccache"], capture_output=True).returncode == 0 else ""

    lines = [
        "# Generated by devtools/vpc_osx.py -- do not edit.",
        "ninja_required_version = 1.7",
        "builddir = %s" % os.path.relpath(out_dir, SRC).replace("$", "$$").replace(" ", "$ "),
        "cc = %s%s" % (ccache, cc),
        "cxx = %s%s" % (ccache, cxx),
        "common = %s" % common,
        "cxxstd = -std=gnu++17 -fdelayed-template-parsing",
        # After the project defines, as on Linux (NINJA_EXTRA_CXX_FLAGS).
        "postflags = -Usprintf -Ustrncpy -UPROTECTED_THINGS_ENABLE",
        "",
        "rule cc",
        "  command = $cc -MMD -MF $out.d $common $flags $postflags -c $in -o $out",
        "  depfile = $out.d",
        "  deps = gcc",
        "  description = CC $in",
        "rule cxx",
        "  command = $cxx -MMD -MF $out.d $common $cxxstd $flags $postflags -c $in -o $out",
        "  depfile = $out.d",
        "  deps = gcc",
        "  description = CXX $in",
        "rule objcxx",
        "  command = $cxx -MMD -MF $out.d $common $cxxstd -x objective-c++ $flags $postflags -c $in -o $out",
        "  depfile = $out.d",
        "  deps = gcc",
        "  description = OBJCXX $in",
        "rule ar",
        "  command = rm -f $out && libtool -static -no_warning_for_no_symbols -o $out $in",
        "  description = AR $out",
        "rule link_dylib",
        "  command = $cxx -arch %s -isysroot %s -mmacosx-version-min=%s -dynamiclib -install_name @rpath/$soname "
        "-Wl,-rpath,@loader_path -Wl,-rpath,@loader_path/../../../bin/%s -o $out $in $libs $ldflags"
        # libsteam_api names itself @loader_path/...; it lives in the engine's bin dir.
        " && install_name_tool -change @loader_path/libsteam_api.dylib @rpath/libsteam_api.dylib $out 2>/dev/null"
        " && codesign --force --sign - $out 2>/dev/null" % (args.arch, sdk, min_os, platdir),
        "  description = LINK $out",
        "rule link_exe",
        "  command = $cxx -arch %s -isysroot %s -mmacosx-version-min=%s -Wl,-rpath,@loader_path "
        "-Wl,-rpath,@loader_path/bin/%s -o $out $in $libs $ldflags"
        " && codesign --force --sign - --entitlements devtools/osx_debug.entitlements $out 2>/dev/null" % (args.arch, sdk, min_os, platdir),
        "  description = LINK $out",
        "rule custom",
        "  command = $cmd",
        "  description = $desc",
        "rule copy",
        "  command = mkdir -p $$(dirname $out) && cp -f $in $out",
        "  description = COPY $out",
        "",
    ]

    all_targets = []
    all_objs = []
    lib_outputs = {}
    project_data = []

    for name in wanted:
        scripts = projects.get(name.lower())
        if not scripts:
            print("vpc_osx: skipping %s (not built on this platform)" % name, file=sys.stderr)
            continue
        script = os.path.join(SRC, scripts[0])
        interp = Interp(conds, dict(macros, PROJECTNAME=name), config)
        interp.run_script(script)
        project_data.append((name, interp))

    for name, interp in project_data:
        proj = interp.project
        tools = proj.tools
        comp = tools.get("compiler", {})
        gen = tools.get("general", {})
        link = tools.get("linker", {})
        libr = tools.get("librarian", {})
        ctype = gen.get("configurationtype", "")
        is_lib = "static" in ctype.lower()
        is_dll = "dynamic" in ctype.lower()

        obj_dir = os.path.join(out_dir, "obj", name)
        defines = split_list(comp.get("preprocessordefinitions"))
        incs = []
        for d in split_list(comp.get("additionalincludedirectories")):
            p = interp.path(d) if not d.startswith("/usr") else None
            if p and p not in incs:
                incs.append(p)
        flags = " ".join("-D" + ninja_escape(shlex.quote(d)) for d in defines)
        flags += " " + " ".join("-I" + ninja_escape(rel(i)) for i in incs)
        flags += " " + comp.get("gcc_extracompilerflags", "").replace("-arch x86_64", "")
        flags += " -isystem " + rel(os.path.join(SRC, "devtools", "osx_include"))
        sysinc = os.environ.get("VPC_OSX_INCLUDES", "/opt/homebrew/include /opt/homebrew/include/SDL2 /opt/homebrew/include/freetype2")
        flags += " " + " ".join("-isystem " + p for p in sysinc.split())
        for fi in comp.get("forceincludes", "").split(";"):
            if fi.strip():
                flags += " -include " + fi.strip()

        pvar = "flags_" + re.sub(r"[^A-Za-z0-9_]", "_", name)
        lines.append("%s = %s" % (pvar, flags))
        flags = "$" + pvar
        gen_headers = []
        objs = []
        sources = []
        for path, entry in proj.files.items():
            if entry.excluded:
                continue
            ext = os.path.splitext(path)[1].lower()
            step = entry.custom if entry.custom.get("commandline") else interp.custom_steps.get(ext.lstrip("."))
            if step and ext != ".proto":
                pdir = interp.base_dir
                name_ = os.path.splitext(os.path.basename(path))[0]

                def msvc(v):
                    v = v.replace("$(InputPath)", path).replace("$(InputName)", name_)
                    v = v.replace("$(InputFileName)", os.path.basename(path))
                    v = v.replace("$(InputDir)", os.path.dirname(path) + "/")
                    return v.replace("\\", "/")
                outs = [os.path.normpath(os.path.join(pdir, msvc(o))) for o in split_list(step.get("outputs"))]
                if not outs:
                    continue
                cmd = msvc(step.get("commandline", "")).replace("python ", "python3 ", 1)
                lines.append("build %s: custom %s" % (" ".join(ninja_escape(rel(o)) for o in outs), ninja_escape(rel(path))))
                lines.append("  cmd = cd %s && %s" % (shlex.quote(pdir), cmd.replace("$", "$$")))
                lines.append("  desc = %s" % msvc(step.get("description", "CUSTOM " + rel(path))).replace("$", "$$"))
                gen_headers += [o for o in outs if not os.path.splitext(o)[1].lower() in SOURCE_EXT]
                sources += [(o, FileEntry(o)) for o in outs if os.path.splitext(o)[1].lower() in SOURCE_EXT]
                continue
            if ext == ".proto":
                gdir = interp.path(interp.macros.get("GENERATED_PROTO_DIR", obj_dir + "/proto"))
                base = os.path.splitext(os.path.basename(path))[0]
                pcc = os.path.join(gdir, base + ".pb.cc")
                ph = os.path.join(gdir, base + ".pb.h")
                protoc = os.path.join(SRC, "thirdparty", "protobuf-2.6.1", "bin", platdir, "protoc")
                extra = interp.macros.get("ADDITIONAL_PROTOC_ARGS", "")
                cmd = ("mkdir -p %s && %s --proto_path=%s --proto_path=%s --proto_path=%s --proto_path=%s %s --cpp_out=%s %s"
                       % (rel(gdir), rel(protoc), "thirdparty/protobuf-2.6.1/src", rel(os.path.dirname(path)),
                          "gcsdk", "game/shared", extra, rel(gdir), rel(path)))
                lines.append("build %s %s: custom %s | %s" % (ninja_escape(rel(pcc)), ninja_escape(rel(ph)),
                                                              ninja_escape(rel(path)), ninja_escape(rel(protoc))))
                lines.append("  cmd = %s" % cmd)
                lines.append("  desc = PROTOC %s" % rel(path))
                gen_headers.append(ph)
                sources.append((pcc, FileEntry(pcc)))
                continue
            if ext in SOURCE_EXT:
                sources.append((path, entry))

        for path, entry in sources:
            ext = os.path.splitext(path)[1].lower()
            rule = SOURCE_EXT[ext]
            obj = os.path.join(obj_dir, os.path.splitext(rel(path).replace("../", "__/"))[0] + ".o")
            fflags = ""
            fd = split_list(entry.compiler.get("preprocessordefinitions"))
            if fd:
                fflags += " " + " ".join("-D" + ninja_escape(shlex.quote(d)) for d in fd if d not in defines)
            for d in split_list(entry.compiler.get("additionalincludedirectories")):
                fflags += " -I" + ninja_escape(rel(interp.path(d)))
            order = (" || " + " ".join(ninja_escape(rel(h)) for h in gen_headers)) if gen_headers else ""
            lines.append("build %s: %s %s%s" % (ninja_escape(rel(obj)), rule, ninja_escape(rel(path)), order))
            lines.append("  flags = %s%s" % (flags, fflags))
            objs.append(obj)

        lines.append("build objs_%s: phony %s" % (re.sub(r"[^A-Za-z0-9_]", "_", name),
                                                  " ".join(ninja_escape(rel(o)) for o in objs)))
        all_objs.append("objs_" + re.sub(r"[^A-Za-z0-9_]", "_", name))
        if is_lib:
            out = interp.path(libr.get("outputfile") or ("$OUTLIBDIR/" + name + ".a"))
            lines.append("build %s: ar %s" % (ninja_escape(rel(out)), " ".join(ninja_escape(rel(o)) for o in objs)))
            lib_outputs[os.path.basename(out)] = out
            all_targets.append(out)
            continue

        # Link libraries.
        libdirs = [os.path.join(SRC, "lib", "public", platdir), os.path.join(SRC, "lib", "common", platdir)]
        libs, deps = [], []
        for kind, p in proj.libs:
            p = p.replace("$(OBJ_DIR)", obj_dir)
            if kind in ("lib", "libexternal"):
                path = interp.path(p + (".a" if not p.endswith(".a") else ""))
                libs.append(rel(path))
                if kind == "lib":
                    deps.append(path)
            else:
                d, b = os.path.split(p)
                if d:
                    dd = interp.path(d)
                    libdirs.append(dd)
                libs.append("-l" + b)
        ldflags = " ".join("-L" + rel(d) for d in libdirs)
        for l in split_list(link.get("systemlibraries")):
            ldflags += " -l" + l
        for fw in split_list(link.get("systemframeworks")):
            ldflags += " -framework " + fw
        ldflags += " " + link.get("gcc_extralinkerflags", "").replace("-arch x86_64", "")
        ldflags += " " + os.environ.get("VPC_OSX_LDFLAGS", "")

        if is_dll:
            out = interp.path(gen.get("gameoutputfile") or link.get("outputfile"))
            rule = "link_dylib"
        else:
            out = interp.path(gen.get("gameoutputfile") or link.get("outputfile") or ("$OUTBINDIR/" + name))
            rule = "link_exe"
        out = out.replace("$(OBJ_DIR)", obj_dir)
        implicit = " | " + " ".join(ninja_escape(rel(d)) for d in deps) if deps else ""
        lines.append("build %s: %s %s%s" % (ninja_escape(rel(out)), rule,
                                            " ".join(ninja_escape(rel(o)) for o in objs), implicit))
        lines.append("  soname = %s" % os.path.basename(out))
        lines.append("  libs = %s" % " ".join(ninja_escape(l) if not l.startswith("-") else l for l in libs))
        lines.append("  ldflags = %s" % ldflags)
        all_targets.append(out)

    lines.append("")
    lines.append("build objs: phony %s" % " ".join(all_objs))
    lines.append("build all: phony %s" % " ".join(ninja_escape(rel(t)) for t in all_targets))
    lines.append("default all")
    out_file = os.path.join(out_dir, "build.ninja")
    with open(out_file, "w") as f:
        f.write("\n".join(lines) + "\n")
    print(rel(out_file))


if __name__ == "__main__":
    main()
