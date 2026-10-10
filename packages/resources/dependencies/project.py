#!/usr/bin/env python3
"""Reuse identical generated packages without a second copy of their sources."""
import pathlib
import re
import shutil
import sys

module = pathlib.Path(sys.argv[1])
core = pathlib.Path(sys.argv[2])
module_owner = sys.argv[3] if len(sys.argv) > 3 else "packages/resources"
roots = [(core, "packages/reconcile")]
if len(sys.argv) > 4:
    roots.append((pathlib.Path(sys.argv[4]), "packages/resources"))
vendor = module / "vendor"


def versions(root):
    result = {}
    for line in (root / "vendor/modules.txt").read_text().splitlines():
        fields = line.split()
        if len(fields) >= 3 and fields[0] == "#" and fields[2].startswith("v"):
            result[fields[1]] = fields[2]
    return result


selected = versions(module)
replacements = {}
remove = []
for shared_root, shared_label in roots:
    shared = versions(shared_root)
    for target in sorted((shared_root / "vendor").rglob("BUCK")):
        package = target.parent.relative_to(shared_root / "vendor").as_posix()
        own = vendor / package / "BUCK"
        if not own.exists():
            continue
        owners = [name for name in shared if package == name or package.startswith(name + "/")]
        owner = max(owners, key=len)
        if selected.get(owner) != shared[owner]:
            raise SystemExit(f"Shared package {package} requires matching module versions")
        name = re.search(r'name = "([^"]+)"', target.read_text()).group(1)
        own_name = re.search(r'name = "([^"]+)"', own.read_text()).group(1)
        replacements[f"//{module_owner}/vendor/{package}:{own_name}"] = (
            f"//{shared_label}/vendor/{package}:{name}"
        )
        remove.append(own.parent)

local_replacements = {
    f"//{module_owner}/vendor/github.com/p5/sir-robs-a-bot/{label}": f"//{label}"
    for _, label in roots
}
for target in sorted(vendor.rglob("BUCK")):
    source = target.read_text()
    for old, new in replacements.items():
        source = source.replace('"' + old + '"', '"' + new + '"')
    for old, new in local_replacements.items():
        source = source.replace(old, new)
    target.write_text(source)

for directory in remove:
    for entry in directory.iterdir():
        if entry.is_file() and not entry.name.startswith(("LICENSE", "COPYING", "NOTICE")):
            entry.unlink()
for _, label in roots:
    local_sources = vendor / f"github.com/p5/sir-robs-a-bot/{label}"
    if local_sources.exists():
        shutil.rmtree(local_sources)
