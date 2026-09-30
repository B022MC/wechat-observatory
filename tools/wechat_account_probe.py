#!/usr/bin/env python3
"""Check the exact current-account bindings against APK method definitions.

Requires androguard. Presence checks prove API shape, not logged-in runtime behavior.
"""

import argparse
import hashlib
import json
from pathlib import Path
import re
import zipfile

RESOLVER = Path(__file__).resolve().parents[1] / (
    "android-module/app/src/main/java/cc/wechat/observatory/wechat/WeChatAccountResolver.java")


def probe_account(apk):
    from loguru import logger
    logger.remove()
    from androguard.core.dex import DEX
    bindings = re.findall(r'\{"([a-z0-9]+\.j1)", "([a-z]+)", "([a-z]+)"\}',
                          RESOLVER.read_text(encoding="utf-8"))
    # Explicit configuration return types are evidence from the verified APKs.
    config_types = {"gm0.j1": "Lcom/tencent/mm/storage/n3;", "gp0.j1": "Lcom/tencent/mm/storage/q3;"}
    targets = {}
    for kernel, factory, getter in bindings:
        prefix = kernel.split(".")[0]
        targets["L%s/j1;" % prefix] = None
        targets["L%s/b0;" % prefix] = None
        targets[config_types[kernel]] = None
    with zipfile.ZipFile(apk) as archive:
        for name in archive.namelist():
            if not name.endswith(".dex"):
                continue
            blob = archive.read(name)
            missing = [key for key, value in targets.items() if value is None and key.encode() in blob]
            if not missing:
                continue
            for cls in DEX(blob).get_classes():
                if cls.get_name() in missing:
                    targets[cls.get_name()] = {
                        (m.get_name(), m.get_descriptor().replace(" ", "")): {
                            "access": m.get_access_flags_string(),
                            "instructions": [i.get_name() + " " + i.get_output()
                                             for i in m.get_instructions()] if m.get_code() else []}
                        for m in cls.get_methods()}
    candidates = []
    for kernel, factory, getter in bindings:
        prefix = kernel.split(".")[0]
        kernel_type, storage_type = "L%s/j1;" % prefix, "L%s/b0;" % prefix
        config_type = config_types[kernel]
        if targets[kernel_type] is None:
            continue
        shapes = [(kernel_type, factory, "()" + storage_type),
                  (storage_type, "g", "()Ljava/lang/String;"),
                  (storage_type, "h", "()Ljava/lang/String;"),
                  (storage_type, "c", "()" + config_type),
                  (config_type, getter, "(ILjava/lang/Object;)Ljava/lang/Object;")]
        methods = []
        for cls, method, descriptor in shapes:
            definition = (targets[cls] or {}).get((method, descriptor))
            methods.append({"method": cls + "->" + method + descriptor, "present": definition is not None})
        factory_def = (targets[kernel_type] or {}).get((factory, "()" + storage_type), {})
        g = (targets[storage_type] or {}).get(("g", "()Ljava/lang/String;"), {})
        h = (targets[storage_type] or {}).get(("h", "()Ljava/lang/String;"), {})
        checks = {
            "static_factory": "static" in factory_def.get("access", ""),
            "path_uses_account_directory": any(storage_type + "->h()" in i for i in g.get("instructions", [])),
            "path_appends_main_db": any('"EnMicroMsg.db"' in i for i in g.get("instructions", [])),
            "path_checks_kernel_account": any("L%s/m;->c()" % prefix in i for i in h.get("instructions", [])),
        }
        candidates.append({"kernel": kernel, "factory": factory, "getter": getter,
                           "methods": methods, "checks": checks,
                           "verified": all(m["present"] for m in methods) and all(checks.values())})
    with open(apk, "rb") as handle:
        digest = hashlib.file_digest(handle, "sha256").hexdigest()
    return {"apk": str(apk), "apk_sha256": digest,
            "identity": "ok" if any(c["verified"] for c in candidates) else "unverified",
            "bindings": candidates, "runtime_validation": "requires a connected logged-in phone"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("apk", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    result = probe_account(args.apk)
    output = json.dumps(result, indent=2, ensure_ascii=False)
    if args.output:
        args.output.write_text(output + "\n", encoding="utf-8")
    print(output)
    raise SystemExit(0 if result["identity"] == "ok" else 1)


if __name__ == "__main__":
    main()
