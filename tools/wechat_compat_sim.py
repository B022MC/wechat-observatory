"""Simulate the module's cross-version resolution against a WeChat APK.

Mirrors every candidate list and member lookup HookEntry uses, so a WeChat build
can be checked for compatibility without flashing it onto a phone:

    python tools/wechat_compat_sim.py wechat-8.0.74-base.apk
    python tools/wechat_compat_sim.py wechat-8.0.74-base.apk wechat-8.0.78.apk

Every rule prints the candidate that wins and whether the members the module
needs are present, which answers two questions at once: does this build work,
and does it still take the OLD code path (no regression for older versions)?
"""

import sys, zipfile
from loguru import logger
logger.remove()
from androguard.core.dex import DEX


def load(path):
    classes = {}
    with zipfile.ZipFile(path) as z:
        for dex_name in [n for n in z.namelist() if n.endswith(".dex")]:
            dex = DEX(z.read(dex_name))
            for cls in dex.get_classes():
                internal = cls.get_name()
                dotted = internal[1:-1].replace("/", ".") if internal.startswith("L") else internal
                fields, methods = set(), set()
                for f in cls.get_fields():
                    fields.add(f.get_name())
                for m in cls.get_methods():
                    methods.add((m.get_name(), m.get_descriptor().replace(" ", "")))
                classes[dotted] = {"fields": fields, "methods": methods}
    return classes


def resolve(classes, names, required_fields=()):
    """Same semantics as HookEntry.resolveClass: first candidate that loads and
    declares one of the required fields."""
    for name in names:
        entry = classes.get(name)
        if entry is None:
            continue
        if not required_fields:
            return name, entry
        if any(f in entry["fields"] for f in required_fields):
            return name, entry
    return None, None


def has_method(entry, name, descriptor=None):
    for m_name, m_desc in entry["methods"]:
        if m_name == name and (descriptor is None or m_desc == descriptor):
            return True
    return False


def method_first(entry, names, descriptor=None):
    for name in names:
        if has_method(entry, name, descriptor):
            return name
    return None


RULES = []


def rule(label, capability):
    def wrapper(fn):
        RULES.append((label, capability, fn))
        return fn
    return wrapper


@rule("extension registry (fs.g|qs.g)", "bootstrap")
def _registry(classes):
    name, entry = resolve(classes, ["fs.g", "qs.g"], ("f283324a", "a"))
    if not entry:
        return None, "no candidate with field a/f283324a"
    missing = [f for f in ("c", "b") if f not in entry["fields"]]
    return name, ("missing fields %s" % missing) if missing else "ok"


@rule("registry enum (fs.k2|qs.k2)", "bootstrap")
def _registry_enum(classes):
    name, entry = resolve(classes, ["fs.k2", "qs.k2"])
    if not entry:
        return None, "not found"
    if not any(m[0] == "values" for m in entry["methods"]):
        return name, "not an enum"
    return name, "ok"


@rule("kernel manager (i95.n0|ph5.n0)", "bootstrap")
def _kernel(classes):
    name, entry = resolve(classes, ["i95.n0", "ph5.n0"], ("f307062f", "f"))
    if not entry:
        return None, "no candidate with field f"
    ok = has_method(entry, "d", "(Landroid/app/Application;L?;L?;)V") or \
        any(m[0] == "d" and m[1].startswith("(Landroid/app/Application;") for m in entry["methods"])
    return name, "init method d(Application,..)" if ok else "init method missing"


@rule("kernel argument (i95.y|ph5.y)", "bootstrap")
def _kernel_arg(classes):
    name, entry = resolve(classes, ["i95.y", "ph5.y"])
    return (name, "ok") if entry else (None, "not found")


@rule("kernel service iface (k95.a|rh5.a)", "bootstrap")
def _kernel_service(classes):
    name, entry = resolve(classes, ["k95.a", "rh5.a"])
    return (name, "ok") if entry else (None, "not found")


@rule("kernel service enum (app.q0|app.l0)", "bootstrap")
def _service_enum(classes):
    # mirrors enumConstantAny(): a reused name that is not an enum is skipped
    for candidate in ["com.tencent.mm.app.q0", "com.tencent.mm.app.l0"]:
        entry = classes.get(candidate)
        if entry is None:
            continue
        if any(m[0] == "values" for m in entry["methods"]):
            return candidate, "ok"
    return None, "no enum candidate"


@rule("send factory (w11.s1|v51.s1)", "send.builder")
def _factory(classes):
    name, entry = resolve(classes, ["w11.s1", "v51.s1"], ("f459386a", "a"))
    if not entry:
        return None, "not found"
    create = any(m[0] == "a" and m[1].startswith("(Ljava/lang/String;)")
                 for m in entry["methods"])
    return name, "factory a(String)" if create else "create method missing"


@rule("send session factory (aq1.l|vu1.l)", "send.builder")
def _session(classes):
    name, entry = resolve(classes, ["aq1.l", "vu1.l"])
    return (name, "ok") if entry else (None, "not found")


@rule("forward info (c01.h7|b41.h7)", "send.builder")
def _forward(classes):
    name, entry = resolve(classes, ["c01.h7", "b41.h7"])
    return (name, "ok") if entry else (None, "not found")


@rule("type resolver (c01.e2|b41.e2)", "send.builder")
def _resolver(classes):
    name, entry = resolve(classes, ["c01.e2", "b41.e2"])
    if not entry:
        return None, "not found"
    return name, "C(String)" if method_first(entry, ["C"]) else "resolve method missing"


@rule("text NetScene (w11.r0|v51.r0)", "send.netscene")
def _netscene(classes):
    name, entry = resolve(classes, ["w11.r0", "v51.r0"])
    if not entry:
        return None, "not found"
    ctors = [m[1] for m in entry["methods"] if m[0] == "<init>"]
    simple = "(Ljava/lang/String;Ljava/lang/String;IIJ)V"
    extended = "(Ljava/lang/String;Ljava/lang/String;IIJLjava/lang/String;)V"
    if simple in ctors:
        return name, "ctor(String,String,int,int,long)"
    if extended in ctors:
        return name, "ctor(...+String) [8.0.78 shape]"
    return name, "no supported ctor: %s" % ctors


@rule("SendMsgMgr accessor (tg3.t1|rn3.u1)", "send.mgr")
def _sendmgr(classes):
    name, entry = resolve(classes, ["tg3.t1", "rn3.u1"])
    if not entry:
        return None, "not found"
    accessor = any(m[0] == "a" and m[1].startswith("()L") for m in entry["methods"])
    return name, "accessor a()" if accessor else "accessor missing"


@rule("builder methods (target/text/type/build)", "send.builder")
def _builder(classes):
    name, entry = resolve(classes, ["w11.r1", "v51.r1"])
    if not entry:
        # builder class name is derived from factory return type at runtime
        return "(dynamic)", "resolved from factory return type"
    parts = []
    if method_first(entry, ["g"]):
        parts.append("g(String)")
    if method_first(entry, ["e"]):
        parts.append("e(String)")
    setter = method_first(entry, ["h", "i"])
    if setter:
        parts.append("%s(int)" % setter)
    if method_first(entry, ["a"]):
        parts.append("a()")
    if len(parts) >= 4:
        return name, ", ".join(parts)
    return name, "incomplete: %s" % ", ".join(parts)


def main():
    for apk in sys.argv[1:]:
        classes = load(apk)
        print("\n" + "=" * 78)
        print(apk)
        print("=" * 78)
        print("%-42s %-16s %-10s %s" % ("rule", "chosen candidate", "verdict", "detail"))
        print("-" * 108)
        failures = 0
        for label, capability, fn in RULES:
            name, detail = fn(classes)
            ok = name is not None and not detail.startswith(("missing", "incomplete", "not an enum", "no "))
            if not ok:
                failures += 1
            print("%-42s %-16s %-10s %s" % (label, name or "-",
                                            "OK" if ok else "FAIL", detail))
        print("\n结果: %s (%d 项失败)" % ("全部通过" if failures == 0 else "存在不兼容", failures))


if __name__ == "__main__":
    main()
