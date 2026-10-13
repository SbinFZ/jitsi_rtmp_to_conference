#!/usr/bin/env python3
"""Generate redistribution notices from the pinned, downloaded Go modules."""
import json
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
raw = subprocess.check_output(["go", "list", "-m", "-json", "all"], cwd=root, text=True)
decoder = json.JSONDecoder()
notices = ["Third-party licenses for jitsi-stream\nVersions: go.mod / go.sum; local signalling fork: third_party/jitsi/UPSTREAM.md\n"]
while raw.strip():
    raw = raw.lstrip()
    module, end = decoder.raw_decode(raw)
    raw = raw[end:]
    if module.get("Main"):
        continue
    metadata = module.get("Replace", module)
    directory = metadata.get("Dir")
    if not directory:
        continue
    paths = sorted(p for p in Path(directory).iterdir() if p.is_file() and p.name.lower().startswith(("license", "copying", "notice")))
    for path in paths:
        notices.append(f"\n{'=' * 72}\n{module['Path']} {module.get('Version', '')} — {path.name}\n\n{path.read_text(errors='replace')}\n")
goroot = Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip())
notices.append(f"\n{'=' * 72}\nGo runtime and standard library — LICENSE\n\n{(goroot / 'LICENSE').read_text()}\n")
out = root / "third_party" / "NOTICES.txt"
out.write_text("\n".join(notices))
print(f"Generated {out.relative_to(root)} ({out.stat().st_size} bytes)")
