"""Member-level mapping between two WeChat builds.

Given class pairs recovered by wechat_pkg_rename.py, match old members to new
members by type signature (names are arbitrary), so the module's hardcoded
field/method names can be translated.

Usage:
    python tools/wechat_member_map.py OLD.apk NEW.apk old.Class=new.Class [more...]
"""

import sys, zipfile, re, collections
from loguru import logger
logger.remove()
from androguard.core.dex import DEX

KEEP = ("com/", "org/", "java/", "javax/", "android/", "kotlin/", "kotlinx/", "io/",
        "okhttp3/", "okio/", "dalvik/", "libcore/", "sun/", "net/", "androidx/")
TYPE_RE = re.compile(r'L([A-Za-z0-9_$]+(?:/[A-Za-z0-9_$]+)+);')


def normalize(descriptor):
    def repl(m):
        internal = m.group(1)
        return "L%s;" % internal if internal.startswith(KEEP) else "L?;"
    return TYPE_RE.sub(repl, descriptor or "")


def describe(path):
    """class -> {'fields': [(name, norm_desc, static)], 'methods': [(name, norm_desc, static)]}"""
    out = {}
    with zipfile.ZipFile(path) as z:
        for dex_name in [n for n in z.namelist() if n.endswith(".dex")]:
            dex = DEX(z.read(dex_name))
            for cls in dex.get_classes():
                internal = cls.get_name()
                dotted = internal[1:-1].replace("/", ".") if internal.startswith("L") else internal
                fields = []
                for f in cls.get_fields():
                    flags = f.get_access_flags_string() or ""
                    fields.append((f.get_name(), normalize(str(f.get_descriptor())), "static" in flags))
                methods = []
                for m in cls.get_methods():
                    flags = m.get_access_flags_string() or ""
                    methods.append((m.get_name(), normalize(m.get_descriptor()), "static" in flags))
                out[dotted] = {"fields": fields, "methods": methods}
    return out


def match_members(old_list, new_list):
    """Greedy match by (normalized descriptor, static) keeping order."""
    buckets = collections.defaultdict(list)
    for name, desc, static in new_list:
        buckets[(desc, static)].append(name)
    result = {}
    for name, desc, static in old_list:
        pool = buckets.get((desc, static)) or []
        if pool:
            result[name] = pool.pop(0)
    return result


def main():
    old_apk, new_apk = sys.argv[1], sys.argv[2]
    pairs = []
    for item in sys.argv[3:]:
        left, _, right = item.partition("=")
        pairs.append((left, right or left))

    print("loading ...")
    old, new = describe(old_apk), describe(new_apk)

    for old_name, new_name in pairs:
        o, n = old.get(old_name), new.get(new_name)
        print("\n" + "=" * 74)
        print("%s  ->  %s" % (old_name, new_name))
        if not o:
            print("  旧包缺少该类")
            continue
        if not n:
            print("  新包缺少该类")
            continue
        fmap = match_members(o["fields"], n["fields"])
        mmap = match_members(o["methods"], n["methods"])
        print("  字段映射：")
        for name, desc, static in o["fields"]:
            print("    %-12s %-28s %-7s -> %s" % (
                name, desc, "static" if static else "", fmap.get(name, "(未匹配)")))
        print("  方法映射：")
        for name, desc, static in o["methods"]:
            print("    %-10s %-46s %-7s -> %s" % (
                name, desc[:46], "static" if static else "", mmap.get(name, "(未匹配)")))


if __name__ == "__main__":
    main()
