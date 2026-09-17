"""Translate obfuscated WeChat hook names from a working build to a new build.

WeChat renames the leading package segment on every release while keeping the
simple class names inside it (w11.s1 -> v51.s1, i95.n0 -> ph5.n0). Fingerprint
matching (superclass + normalised member signatures) recovers that rename, so a
working build's hook names can be translated mechanically.

Usage:
    python tools/wechat_pkg_rename.py OLD.apk NEW.apk w11.s1 w11.r0 tg3.t1 ...
"""

import sys, zipfile, re, collections
from loguru import logger
logger.remove()
from androguard.core.dex import DEX

KEEP_PREFIXES = ("com/", "org/", "java/", "javax/", "android/", "kotlin/", "kotlinx/",
                 "io/", "okhttp3/", "okio/", "dalvik/", "libcore/", "sun/", "net/",
                 "androidx/")
TYPE_RE = re.compile(r'L([A-Za-z0-9_$]+(?:/[A-Za-z0-9_$]+)+);')


def normalize(descriptor):
    def repl(match):
        internal = match.group(1)
        return "L%s;" % internal if internal.startswith(KEEP_PREFIXES) else "L?;"
    return TYPE_RE.sub(repl, descriptor or "")


def load(path):
    classes = {}
    with zipfile.ZipFile(path) as z:
        for dex_name in [n for n in z.namelist() if n.endswith(".dex")]:
            dex = DEX(z.read(dex_name))
            for cls in dex.get_classes():
                internal = cls.get_name()
                dotted = internal[1:-1].replace("/", ".") if internal.startswith("L") else internal
                methods = {m.get_name() + normalize(m.get_descriptor()) for m in cls.get_methods()}
                fields = {f.get_name() + ":" + normalize(str(f.get_descriptor()))
                          for f in cls.get_fields()}
                supername = cls.get_superclassname() or ""
                classes[dotted] = {
                    "super": normalize(supername),
                    "sigs": methods | fields,
                    "size": len(methods) + len(fields),
                }
    return classes


def similarity(a, b):
    if not a["sigs"] or not b["sigs"]:
        return 0.0
    return len(a["sigs"] & b["sigs"]) / float(len(a["sigs"] | b["sigs"]))


def best_match(old_entry, new_by_super):
    candidates = new_by_super.get(old_entry["super"], [])
    best = (0.0, None)
    for name, entry in candidates:
        score = similarity(old_entry, entry)
        if score > best[0]:
            best = (score, name)
    return best


def main():
    old_apk, new_apk = sys.argv[1], sys.argv[2]
    wanted = sys.argv[3:]
    print("loading %s ..." % old_apk)
    old = load(old_apk)
    print("loading %s ..." % new_apk)
    new = load(new_apk)

    new_by_super = collections.defaultdict(list)
    for name, entry in new.items():
        new_by_super[entry["super"]].append((name, entry))

    packages = []
    for item in wanted:
        pkg = item.rsplit(".", 1)[0] if "." in item else item
        if pkg not in packages:
            packages.append(pkg)

    # 先做逐类匹配
    per_class = {}
    for name in wanted:
        entry = old.get(name)
        if not entry:
            per_class[name] = (0.0, None, "旧包中不存在")
            continue
        score, match = best_match(entry, new_by_super)
        per_class[name] = (score, match, "")

    # 包级投票：用同包内成员多的类来确定改名
    renames = {}
    for pkg in packages:
        votes = collections.Counter()
        for name, entry in old.items():
            if not name.startswith(pkg + ".") or entry["size"] < 5:
                continue
            score, match = best_match(entry, new_by_super)
            if match and score >= 0.7:
                votes[match.rsplit(".", 1)[0]] += 1
        if votes:
            best_pkg, count = votes.most_common(1)[0]
            renames[pkg] = (best_pkg, count, sum(votes.values()))

    print("\n=== 包改名 ===")
    for pkg, (npkg, count, total) in sorted(renames.items()):
        print("  %-8s -> %-8s  (投票 %d/%d)" % (pkg, npkg, count, total))

    print("\n=== 类映射 ===")
    for name in wanted:
        score, match, note = per_class[name]
        if match:
            print("  %-14s -> %-22s 相似度=%.2f" % (name, match, score))
        else:
            print("  %-14s -> (无候选) %s" % (name, note))

    print("\n=== 按包改名推导（同简单名）===")
    for name in wanted:
        if "." not in name:
            continue
        pkg, simple = name.rsplit(".", 1)
        if pkg in renames:
            guess = "%s.%s" % (renames[pkg][0], simple)
            exists = guess in new
            print("  %-14s -> %-22s %s" % (name, guess, "存在" if exists else "**不存在**"))


if __name__ == "__main__":
    main()
