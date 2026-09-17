"""Structural fingerprint matching for WeChat version adaptation.

Obfuscated class names change every WeChat build, but a class keeps its shape:
superclass, interfaces and the (mostly non-obfuscated) types it touches. This
tool fingerprints the hook classes in a working build and searches a new build
for the class that still matches.
"""
import re, sys, json
from loguru import logger
logger.remove()
from androguard.core.dex import DEX

KEEP_PREFIXES = ("com/", "org/", "java/", "javax/", "android/", "kotlin/", "kotlinx/",
                 "io/", "okhttp3/", "okio/", "dalvik/", "libcore/", "sun/", "junit/",
                 "net/", "androidx/", "org/json")
TYPE_RE = re.compile(r'L([A-Za-z0-9_$]+(?:/[A-Za-z0-9_$]+)+);')

def normalize(descriptor: str) -> str:
    """Replace obfuscated type references with ?, keep well-known packages."""
    def repl(match):
        internal = match.group(1)
        if internal.startswith(KEEP_PREFIXES):
            return "L%s;" % internal
        return "L?;"
    return TYPE_RE.sub(repl, descriptor)

def load_classes(path):
    import zipfile
    out = {}
    with zipfile.ZipFile(path) as z:
        for name in [n for n in z.namelist() if n.endswith(".dex")]:
            dex = DEX(z.read(name))
            for cls in dex.get_classes():
                internal = cls.get_name()
                dotted = internal[1:-1].replace("/", ".") if internal.startswith("L") else internal
                methods, fields = set(), set()
                for m in cls.get_methods():
                    methods.add(m.get_name() + normalize(m.get_descriptor()))
                for f in cls.get_fields():
                    fields.add(f.get_name() + ":" + normalize(str(f.get_descriptor())))
                supername = cls.get_superclassname() or ""
                out[dotted] = {
                    "super": normalize(supername) if supername else "",
                    "interfaces": sorted(normalize(i) for i in (cls.get_interfaces() or [])),
                    "methods": methods,
                    "fields": fields,
                }
    return out

def fingerprint(entry):
    return entry["methods"] | entry["fields"]

def similarity(a, b):
    fa, fb = fingerprint(a), fingerprint(b)
    if not fa or not fb:
        return 0.0
    inter = len(fa & fb)
    return inter / float(len(fa | fb))

def main():
    old_apk, new_apk = sys.argv[1], sys.argv[2]
    targets = sys.argv[3:]
    print("loading %s ..." % old_apk)
    old = load_classes(old_apk)
    print("loading %s ..." % new_apk)
    new = load_classes(new_apk)
    print("classes: old=%d new=%d\n" % (len(old), len(new)))

    for target in targets:
        src = old.get(target)
        if not src:
            print("== %s : 在旧包中不存在" % target)
            continue
        scored = []
        for name, entry in new.items():
            if entry.get("super") != src["super"]:
                continue
            score = similarity(src, entry)
            if score > 0.35:
                scored.append((score, name, entry))
        scored.sort(reverse=True)
        print("== %s  方法数=%d 字段数=%d 父类=%s" % (
            target, len(src["methods"]), len(src["fields"]), src["super"]))
        if not scored:
            print("   无候选（父类 %s 下没有结构相似度 > 0.35 的类）" % src["super"])
        for score, name, entry in scored[:6]:
            print("   %.2f  %-38s methods=%d fields=%d" % (
                score, name, len(entry["methods"]), len(entry["fields"])))
        print()

if __name__ == "__main__":
    main()
