#!/usr/bin/env python3
"""WeChat hook-point probe for the observatory LSPosed module.

The module talks to WeChat through hardcoded internal class/field names.
WeChat re-obfuscates those names with a fresh random seed on every release, so
a build that used to work can silently break. This tool answers one question
before anything ships:

    "Does this WeChat APK still contain every hook point the module needs?"

Usage
-----
    python tools/wechat_hook_probe.py path/to/weixin.apk [more.apk ...]
    python tools/wechat_hook_probe.py weixin.apk --json profile.json
    python tools/wechat_hook_probe.py weixin.apk --shape-scan
    python tools/wechat_hook_probe.py weixin.apk --source path/to/HookEntry.java

Exit code is 0 when every required hook point is present, 1 otherwise, so the
tool can gate CI or a release pipeline.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

# ---------------------------------------------------------------------------
# Hook points
#
# "stable" entries are non-obfuscated framework classes: they survive WeChat
# releases and must always be present. "obfuscated" entries are the ones WeChat
# renames every build; a missing entry means the matching capability is dead.
# ---------------------------------------------------------------------------

STABLE = [
    ("observation", "class", "com.tencent.wcdb.database.SQLiteDatabase",
     "WCDB database class used for message observation"),
    ("observation", "field", "sActiveDatabases",
     "static map of open WCDB databases"),
    ("observation", "method", "insertWithOnConflict",
     "message table insert hook"),
    ("observation", "class", "com.tencent.wcdb.support.CancellationSignal",
     "rawQuery overload signature"),
    ("identity", "sql", "SELECT value FROM userinfo",
     "current account row query"),
    ("send", "class", "com.tencent.mm.modelbase.z2",
     "NetScene queue entry point"),
    ("send", "class", "com.tencent.mm.modelbase.m1",
     "NetScene queue element type"),
    ("send", "class", "com.tencent.mm.autogen.events.SendMsgEvent",
     "autogen send event (non-obfuscated bus)"),
]

# (capability, kind, token, role) - token is the dotted name from the module
OBFUSCATED = [
    ("bootstrap", "class", "fs.g", "extension registry"),
    ("bootstrap", "field", "f283324a", "extension registry array slot"),
    ("bootstrap", "enum", "fs.k2", "registry application enum"),
    ("bootstrap", "class", "com.tencent.mm.app.p0", "kernel service provider"),
    ("bootstrap", "field", "f70808d", "kernel service provider field"),
    ("bootstrap", "class", "i95.n0", "service manager readiness flag"),
    ("bootstrap", "field", "f307062f", "service manager readiness field"),
    ("bootstrap", "class", "i95.y", "service manager argument type"),
    ("bootstrap", "class", "k95.a", "kernel service interface"),
    ("bootstrap", "class", "com.tencent.mm.sdk.platformtools.x2",
     "MMApplicationContext"),
    ("bootstrap", "field", "f210311a", "MMApplicationContext application field"),
    ("send", "class", "w11.s1", "send builder factory (primary path)"),
    ("send", "class", "w11.r1", "message builder"),
    ("send", "class", "w11.n1", "NetScene queue admission"),
    ("send", "class", "w11.r0", "text message NetScene"),
    ("send", "field", "f459357f", "local message id field"),
    ("send", "class", "tg3.t1", "SendMsgMgr accessor"),
    ("send", "class", "dk5.s5", "SendMsgMgr implementation"),
    ("send", "class", "c01.h7", "send payload object"),
    ("send", "class", "c01.e2", "send type resolver"),
    ("send", "class", "aq1.l", "session factory"),
    ("send", "field", "f71992g", "SendMsgEvent payload field"),
    ("send", "field", "f7337a", "SendMsgEvent target wxid field"),
    ("send", "field", "f7338b", "SendMsgEvent text field"),
    ("send", "field", "f7339c", "SendMsgEvent type field"),
    ("send", "field", "f7340d", "SendMsgEvent flag field"),
]

SOURCE_PATTERNS = [
    # XposedHelpers.findClass("com.tencent...", ...)
    re.compile(r'findClass\(\s*(?:[A-Za-z_][\w.]*\s*,\s*)?"([^"]+)"'),
    # isStaticArraySlotReady(classLoader, "fs.g", "f283324a", "a")
    re.compile(r'isStaticArraySlotReady\([^,]+,\s*"([^"]+)"\s*,\s*"([^"]+)"'),
    # isStaticBooleanFlag(classLoader, "i95.n0", "f307062f", "f")
    re.compile(r'isStaticBooleanFlag\([^,]+,\s*"([^"]+)"\s*,\s*"([^"]+)"'),
    # findFieldAny(<expr>, "f210311a", "a") - first literal only
    re.compile(r'findFieldAny\([^,]+,\s*"(f[0-9a-f]{6,})"'),
]

SHAPE_FAMILIES = [
    "(Ljava/lang/String;Ljava/lang/String;IIJ)V",
    "(Ljava/lang/String;Ljava/lang/String;II)V",
    "(Ljava/lang/String;Ljava/lang/String;III)V",
    "(Ljava/lang/String;Ljava/lang/String;ILjava/lang/String;)V",
    "(Ljava/lang/String;Ljava/lang/String;IZ)V",
]

# Capability verdicts are path based: the module only needs ONE working send
# path, and a missing obfuscated *field* is not fatal because the module resolves
# fields through fallback candidate lists (findFieldAny). Class tokens gate.
OBSERVATION_TOKENS = [
    "com.tencent.wcdb.database.SQLiteDatabase",
    "sActiveDatabases",
    "insertWithOnConflict",
    "com.tencent.wcdb.support.CancellationSignal",
]
IDENTITY_TOKENS = ["SELECT value FROM userinfo"]
SEND_PATHS = [
    ("builder", ["w11.s1", "w11.r1", "w11.n1"]),
    ("netscene", ["w11.r0", "com.tencent.mm.modelbase.z2", "com.tencent.mm.modelbase.m1"]),
    ("event", ["com.tencent.mm.autogen.events.SendMsgEvent"]),
    ("sendmgr", ["tg3.t1", "dk5.s5"]),
]
BOOTSTRAP_CLASSES = ["fs.g", "i95.n0", "i95.y", "k95.a", "com.tencent.mm.app.p0",
                     "com.tencent.mm.sdk.platformtools.x2"]

# Field/enum names are regenerated by WeChat on every build, so their absence is
# reported as advisory rather than failing the probe.
ADVISORY_KINDS = {"field", "enum", "method"}


def extract_source_tokens(source: Path) -> list[tuple[str, str]]:
    """Pull class/field literals out of HookEntry.java so tokens never drift."""
    text = source.read_text(encoding="utf-8", errors="replace")
    found: list[tuple[str, str]] = []
    seen: set[str] = set()
    for pattern in SOURCE_PATTERNS:
        for match in pattern.finditer(text):
            for literal in match.groups():
                if literal and literal not in seen and "." in literal or (
                        literal and literal.startswith("f") and len(literal) >= 8):
                    if literal in seen:
                        continue
                    seen.add(literal)
                    found.append((literal, source.name))
    return found


def load_dex_blobs(apk: Path) -> list[bytes]:
    with zipfile.ZipFile(apk) as archive:
        names = sorted(n for n in archive.namelist() if n.endswith(".dex"))
        return [archive.read(n) for n in names]


def token_present(blobs: list[bytes], token: str) -> bool:
    """Dex stores class descriptors as Lpkg/Class; fields/methods as plain names."""
    slash = token.replace(".", "/")
    needles = {token.encode(), slash.encode(), ("L" + slash + ";").encode()}
    for blob in blobs:
        for needle in needles:
            if needle in blob:
                return True
    return False


def dex_digest(blobs: list[bytes]) -> str:
    digest = hashlib.sha256()
    for blob in blobs:
        digest.update(blob)
    return "sha256:" + digest.hexdigest()[:32]


def adb(*args: str, timeout: int = 300) -> str:
    exe = shutil.which("adb") or "adb"
    proc = subprocess.run([exe, *args], capture_output=True, text=True,
                          encoding="utf-8", errors="replace", timeout=timeout)
    if proc.returncode != 0:
        raise RuntimeError("adb %s failed: %s" % (" ".join(args), (proc.stderr or proc.stdout).strip()))
    return proc.stdout


def pull_device_apk(package: str = "com.tencent.mm", dest_dir: Path | None = None) -> Path:
    """Copy the installed APK off a USB-connected device.

    WeChat's install path contains ``~~<hash>==`` segments, so the path is taken
    verbatim from `pm path` rather than assembled by hand.
    """
    out = adb("shell", "pm", "path", package)
    paths = [line.split(":", 1)[1].strip() for line in out.splitlines() if line.startswith("package:")]
    base = next((p for p in paths if p.endswith("base.apk")), None)
    if not base:
        raise RuntimeError("no base.apk reported by pm path for %s" % package)
    dest_dir = dest_dir or Path.cwd()
    dest_dir.mkdir(parents=True, exist_ok=True)
    local = dest_dir / ("%s-%s" % (package.replace(".", "-"), base.split("==")[-1].strip("/")))
    local = local.with_suffix(".apk")
    adb("pull", base, str(local))
    return local


def class_members(apk: Path, class_names: list[str]) -> dict[str, dict[str, list[str]]]:
    """Declared methods/fields per class, for diffing two WeChat builds.

    Needs androguard; used only by --compare.
    """
    try:
        from loguru import logger
        logger.remove()
        from androguard.core.dex import DEX
    except Exception as exc:  # pragma: no cover - optional dependency
        raise RuntimeError("--compare needs androguard (pip install androguard): %s" % exc)

    wanted = set(class_names)
    found: dict[str, dict[str, list[str]]] = {}
    with zipfile.ZipFile(apk) as archive:
        for name in [n for n in archive.namelist() if n.endswith(".dex")]:
            dex = DEX(archive.read(name))
            for cls in dex.get_classes():
                internal = cls.get_name()
                dotted = internal[1:-1].replace("/", ".") if internal.startswith("L") else internal
                if dotted not in wanted:
                    continue
                entry = found.setdefault(dotted, {"methods": [], "fields": []})
                for method in cls.get_methods():
                    entry["methods"].append(method.get_name() + method.get_descriptor())
                for field in cls.get_fields():
                    entry["fields"].append(field.get_name() + ":" + str(field.get_descriptor()))
    for entry in found.values():
        entry["methods"] = sorted(set(entry["methods"]))
        entry["fields"] = sorted(set(entry["fields"]))
    return found


def compare_versions(old_apk: Path, new_apk: Path, class_names: list[str]) -> dict:
    old_present = {n: token_present(load_dex_blobs(old_apk), n) for n in class_names}
    new_present = {n: token_present(load_dex_blobs(new_apk), n) for n in class_names}
    lost = [n for n in class_names if old_present[n] and not new_present[n]]
    kept = [n for n in class_names if old_present[n] and new_present[n]]
    gained = [n for n in class_names if not old_present[n] and new_present[n]]
    old_members = class_members(old_apk, kept)
    new_members = class_members(new_apk, kept)
    changed = {}
    for name in kept:
        before, after = old_members.get(name, {}), new_members.get(name, {})
        diff = {
            "methodsRemoved": sorted(set(before.get("methods", [])) - set(after.get("methods", []))),
            "methodsAdded": sorted(set(after.get("methods", [])) - set(before.get("methods", []))),
            "fieldsRemoved": sorted(set(before.get("fields", [])) - set(after.get("fields", []))),
            "fieldsAdded": sorted(set(after.get("fields", [])) - set(before.get("fields", []))),
        }
        if any(diff.values()):
            changed[name] = diff
    return {"lost": lost, "kept": kept, "gained": gained, "changed": changed}


def print_comparison(old_apk: Path, new_apk: Path, report: dict) -> None:
    print("=" * 78)
    print("compare: %s  ->  %s" % (old_apk.name, new_apk.name))
    print("=" * 78)
    if report["lost"]:
        print("\n[类在新版本中消失 —— 需要重新定位]")
        for name in report["lost"]:
            print("  LOST  %s" % name)
    if report["gained"]:
        print("\n[新版本新增的候选类]")
        for name in report["gained"]:
            print("  NEW   %s" % name)
    if report["changed"]:
        print("\n[类还在，但成员变了 —— 方法/字段被改名]")
        for name, diff in report["changed"].items():
            print("  %s" % name)
            for key in ("methodsRemoved", "methodsAdded", "fieldsRemoved", "fieldsAdded"):
                values = diff.get(key) or []
                if values:
                    print("      %-15s %s" % (key, ", ".join(values[:8]) +
                                              (" ..." if len(values) > 8 else "")))
    if not report["lost"] and not report["changed"] and not report["gained"]:
        print("\n无差异：这些 hook 点在新版本中完全一致。")


def find_aapt2() -> str | None:
    """Locate aapt2 without requiring ANDROID_HOME; version info is optional."""
    found = shutil.which("aapt2")
    if found:
        return found
    roots = []
    for var in ("ANDROID_HOME", "ANDROID_SDK_ROOT"):
        if os.environ.get(var):
            roots.append(Path(os.environ[var]))
    home = Path.home()
    roots += [
        home / "AppData/Local/Android/Sdk",
        home / "Android/Sdk",
        Path("/usr/lib/android-sdk"),
        Path("/opt/android-sdk"),
        Path("/usr/local/lib/android/sdk"),
    ]
    for root in roots:
        try:
            candidates = [p for p in (root / "build-tools").glob("*/aapt2*") if p.is_file()]
        except Exception:
            continue
        if candidates:
            # prefer the newest build-tools revision
            return str(sorted(candidates, key=lambda p: p.parent.name, reverse=True)[0])
    return None


def _version_name_from_manifest(apk: Path) -> str | None:
    """WeChat's manifest string pool stores its version as UTF-16 (e.g. 8.0.78)."""
    try:
        data = zipfile.ZipFile(apk).read("AndroidManifest.xml")
    except Exception:
        return None
    text = data.decode("utf-16-le", errors="ignore")
    found = re.findall(r"\b8\.\d{1,2}\.\d{1,2}\b", text)
    if not found:
        return None
    return max(set(found), key=found.count)


def apk_version(apk: Path) -> tuple[str | None, str | None]:
    code = name = None
    aapt2 = find_aapt2()
    if aapt2:
        try:
            out = subprocess.run([aapt2, "dump", "badging", str(apk)],
                                 capture_output=True, text=True, encoding="utf-8",
                                 errors="replace", timeout=180).stdout
            code_match = re.search(r"versionCode='(\d+)'", out)
            name_match = re.search(r"versionName='([^']+)'", out)
            code = code_match.group(1) if code_match else None
            name = name_match.group(1) if name_match else None
        except Exception:
            pass
    if name is None:
        name = _version_name_from_manifest(apk)
    return code, name


def shape_scan(apk: Path) -> dict[str, list[str]]:
    """Optional structural scan; needs `pip install androguard`."""
    try:
        from loguru import logger
        logger.remove()
        from androguard.core.dex import DEX
    except Exception as exc:  # pragma: no cover - optional dependency
        return {"error": ["androguard unavailable: %s" % exc]}
    hits: dict[str, list[str]] = {shape: [] for shape in SHAPE_FAMILIES}
    with zipfile.ZipFile(apk) as archive:
        for name in [n for n in archive.namelist() if n.endswith(".dex")]:
            dex = DEX(archive.read(name))
            for cls in dex.get_classes():
                for method in cls.get_methods():
                    descriptor = method.get_descriptor()
                    if descriptor in hits and len(hits[descriptor]) < 12:
                        hits[descriptor].append(cls.get_name() + "::" + method.get_name())
    return {k: v for k, v in hits.items() if v}


def probe(apk: Path, source: Path | None, want_shapes: bool) -> dict:
    blobs = load_dex_blobs(apk)
    code, name = apk_version(apk)
    rows = []
    for capability, kind, token, role in STABLE:
        rows.append({
            "capability": capability, "kind": kind, "token": token, "role": role,
            "stable": True, "present": token_present(blobs, token),
        })
    for capability, kind, token, role in OBFUSCATED:
        rows.append({
            "capability": capability, "kind": kind, "token": token, "role": role,
            "stable": False, "present": token_present(blobs, token),
        })
    extra = []
    if source and source.exists():
        known = {r["token"] for r in rows}
        for literal, _ in extract_source_tokens(source):
            if literal in known:
                continue
            extra.append({
                "capability": "source-only", "kind": "literal", "token": literal,
                "role": "referenced in HookEntry.java", "stable": False,
                "present": token_present(blobs, literal),
            })
    result = {
        "apk": str(apk),
        "versionCode": code,
        "versionName": name,
        "dexCount": len(blobs),
        "dexHash": dex_digest(blobs),
        "hooks": rows + extra,
    }
    if want_shapes:
        result["shapeCandidates"] = shape_scan(apk)
    return result


def present_map(result: dict) -> dict[str, bool]:
    return {row["token"]: row["present"] for row in result["hooks"]}


def complete_paths(present: dict[str, bool]) -> list[str]:
    return [name for name, tokens in SEND_PATHS
            if all(present.get(token) for token in tokens)]


def summarize(result: dict) -> dict[str, str]:
    """Capability verdicts.

    observation/identity/bootstrap gate the module:

    * observation and identity use non-obfuscated names, so a miss is fatal;
    * bootstrap gates the send stack - HookEntry.isWeChatReadyForSend() refuses
      to poll the outbox unless the fs.g registry slot and the i95.n0 kernel flag
      resolve, which is what actually breaks the module on new WeChat builds;
    * send lists which paths still have all of their classes, purely
      informational: obfuscated *field* names are unreliable indicators (they are
      already absent on versions that work, because the module falls back to
      short field names).

    Verified against on-device reports: 8.0.74 -> bootstrap ok and the module
    reports every path ok; 8.0.76 / 8.0.78 -> bootstrap incomplete and the module
    skips outbox delivery.
    """
    present = present_map(result)
    observation = "ok" if all(present.get(t) for t in OBSERVATION_TOKENS) else "broken"
    identity = "ok" if all(present.get(t) for t in IDENTITY_TOKENS) else "broken"
    bootstrap_missing = [t for t in BOOTSTRAP_CLASSES if not present.get(t)]
    bootstrap = "ok" if not bootstrap_missing else "incomplete(%s)" % ",".join(bootstrap_missing)
    paths = complete_paths(present) or ["none"]
    send = "paths:" + ",".join(paths)
    return {"observation": observation, "identity": identity,
            "send": send, "bootstrap": bootstrap}


def advisory_missing(result: dict) -> list[str]:
    return [row["token"] for row in result["hooks"]
            if not row["present"] and row["kind"] in ADVISORY_KINDS]


def print_report(result: dict) -> None:
    title = "%s  (%s / %s, %d dex)" % (
        Path(result["apk"]).name, result["versionName"] or "?",
        result["versionCode"] or "?", result["dexCount"])
    print(title)
    print("=" * len(title))
    current = None
    for row in result["hooks"]:
        if row["capability"] != current:
            current = row["capability"]
            print("\n[%s]" % current)
        advisory = " (advisory)" if row["kind"] in ADVISORY_KINDS else ""
        mark = "OK  " if row["present"] else "MISS"
        print("  %-5s %-9s %-42s %s%s" % (mark, row["kind"], row["token"], row["role"], advisory))
    print("\ncapabilities: %s" % json.dumps(summarize(result), ensure_ascii=False))
    advisory = advisory_missing(result)
    if advisory:
        print("advisory misses (%d, module has fallback names): %s"
              % (len(advisory), ", ".join(advisory[:12]) + (" ..." if len(advisory) > 12 else "")))
    shapes = result.get("shapeCandidates")
    if shapes:
        print("\nshape candidates (for structural resolution):")
        for shape, hits in shapes.items():
            print("  %s" % shape)
            for hit in hits[:8]:
                print("      %s" % hit)


def build_profile(result: dict) -> dict:
    """Turn a probe result into a version profile the module can consume."""
    present = {row["token"]: row["present"] for row in result["hooks"]}

    def pick(*names):
        for name in names:
            if present.get(name):
                return name
        return None

    def entry(**fields):
        resolved = {k: v for k, v in fields.items() if v}
        missing = [k for k, v in fields.items() if not v]
        if missing:
            resolved["unresolved"] = missing
        return resolved

    send_paths = [
        entry(id="builder",
              factory=pick("w11.s1"), builder=pick("w11.r1"),
              queue=pick("w11.n1"), localIdField=pick("f459357f")),
        entry(id="netscene", **{"class": pick("w11.r0")},
              ctor="(Ljava/lang/String;Ljava/lang/String;IIJ)V",
              enqueue=pick("com.tencent.mm.modelbase.z2"),
              queueElement=pick("com.tencent.mm.modelbase.m1"),
              localIdField=pick("f459357f")),
        entry(id="event", **{"class": pick("com.tencent.mm.autogen.events.SendMsgEvent")},
              payloadField=pick("f71992g"),
              payloadWxid=pick("f7337a"), payloadText=pick("f7338b"),
              payloadType=pick("f7339c"), payloadFlag=pick("f7340d")),
        entry(id="sendmgr", accessor=pick("tg3.t1"), impl=pick("dk5.s5"),
              method="(Ljava/lang/String;Ljava/lang/String;II)V"),
    ]
    paths = complete_paths(present)
    return {
        "schema": 1,
        "generatedBy": "tools/wechat_hook_probe.py",
        "wechat": {
            "versionName": result.get("versionName"),
            "versionCode": result.get("versionCode"),
        },
        "dexHash": result.get("dexHash"),
        "capabilities": summarize(result),
        "completePaths": paths,
        "advisoryMissing": advisory_missing(result),
        "identity": {
            "idQueries": [
                "SELECT value FROM userinfo WHERE id=2 LIMIT 1",
                "SELECT value FROM userinfo WHERE id=42 LIMIT 1",
            ],
            "nicknameQueries": [
                "SELECT value FROM userinfo WHERE id=4 LIMIT 1",
                "SELECT value FROM userinfo WHERE id=5 LIMIT 1",
                "SELECT value FROM userinfo WHERE id=6 LIMIT 1",
            ],
        },
        "observation": {
            "class": "com.tencent.wcdb.database.SQLiteDatabase",
            "activeDatabasesField": "sActiveDatabases",
            "insertMethod": "insertWithOnConflict",
        },
        "send": {
            "preferred": next((p["id"] for p in send_paths if "unresolved" not in p), None),
            "paths": send_paths,
            "bootstrap": {
                "registry": pick("fs.g"),
                "registrySlot": pick("f283324a"),
                "kernel": pick("i95.n0"),
                "kernelFlag": pick("f307062f"),
                "provider": pick("com.tencent.mm.app.p0"),
                "providerField": pick("f70808d"),
            },
        },
        "missing": sorted(t for t, ok in present.items() if not ok),
    }


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="Probe a WeChat APK for module hook points")
    parser.add_argument("apk", nargs="*", help="WeChat APK file(s)")
    parser.add_argument("--from-device", action="store_true",
                        help="pull the installed WeChat APK off the USB device first")
    parser.add_argument("--device-dir", default=".wechat-apks",
                        help="where --from-device stores the pulled APK (default .wechat-apks)")
    parser.add_argument("--compare", metavar="OLD_APK",
                        help="diff hook classes against a previously working WeChat APK")
    parser.add_argument("--compare-classes", nargs="*", default=None,
                        help="classes to diff with --compare (default: the hook points)")
    parser.add_argument("--source", help="HookEntry.java used to cross-check literals")
    parser.add_argument("--json", help="write the raw probe result as JSON")
    parser.add_argument("--profile",
                        help="write a version profile JSON (one file per APK when "
                             "several are given, suffixed by versionName)")
    parser.add_argument("--shape-scan", action="store_true",
                        help="also look for send-path method shapes (needs androguard)")
    args = parser.parse_args(argv)

    source = Path(args.source) if args.source else None
    if source is None:
        candidate = Path(__file__).resolve().parents[1] / \
            "android-module/app/src/main/java/cc/wechat/observatory/HookEntry.java"
        if candidate.exists():
            source = candidate

    apk_args = list(args.apk)
    if args.from_device:
        try:
            pulled = pull_device_apk(dest_dir=Path(args.device_dir))
            print("pulled %s\n" % pulled)
            apk_args.append(str(pulled))
        except Exception as exc:
            print("device pull failed: %s" % exc, file=sys.stderr)
            return 1
    if not apk_args:
        parser.error("no APK given (pass a path or use --from-device)")

    results = []
    failed = False
    for apk in apk_args:
        path = Path(apk)
        if not path.exists():
            print("no such file: %s" % path, file=sys.stderr)
            failed = True
            continue
        result = probe(path, source, args.shape_scan)
        results.append(result)
        print_report(result)
        print()
        verdict = summarize(result)
        # observation/identity/bootstrap gate the module; send is informational.
        if verdict["observation"] != "ok" or verdict["identity"] != "ok" \
                or verdict["bootstrap"] != "ok":
            failed = True

    if args.compare and results:
        hook_classes = args.compare_classes
        if not hook_classes:
            hook_classes = sorted({row["token"] for row in results[0]["hooks"]
                                   if row["kind"] == "class" and "." in row["token"]})
        report = compare_versions(Path(args.compare), Path(results[0]["apk"]), hook_classes)
        print_comparison(Path(args.compare), Path(results[0]["apk"]), report)

    if args.json:
        payload = results[0] if len(results) == 1 else results
        Path(args.json).write_text(json.dumps(payload, ensure_ascii=False, indent=2),
                                   encoding="utf-8")
        print("wrote %s" % args.json)

    if args.profile:
        target = Path(args.profile)
        for result in results:
            profile = build_profile(result)
            if len(results) == 1:
                out = target
            else:
                suffix = (result.get("versionName") or "unknown").replace("/", "_")
                out = target.with_name("%s.%s%s" % (target.stem, suffix, target.suffix or ".json"))
            out.parent.mkdir(parents=True, exist_ok=True)
            out.write_text(json.dumps(profile, ensure_ascii=False, indent=2), encoding="utf-8")
            print("wrote profile %s (send=%s, complete paths=%s, advisory misses=%d)" % (
                out, profile["capabilities"]["send"],
                ",".join(profile["completePaths"]) or "none",
                len(profile["advisoryMissing"])))

    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
