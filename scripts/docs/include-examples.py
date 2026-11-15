#!/usr/bin/env python3
"""Fills every `<!-- include:PATH LANG -->` block of the docs pages with the
file's content, so the pages cannot drift from the example files."""
import pathlib
import re
import subprocess
import sys

root = pathlib.Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip())
pattern = re.compile(r"(<!-- include:(\S+) (\S+) -->\n).*?(<!-- /include -->)", re.S)


def fill(match):
    body = (root / match.group(2)).read_text()
    return f"{match.group(1)}```{match.group(3)}\n{body.rstrip(chr(10))}\n```\n{match.group(4)}"


for page in sorted((root / "docs").rglob("*.md")):
    if "agents" in page.relative_to(root / "docs").parts:
        continue
    text = page.read_text()
    new = pattern.sub(fill, text)
    if new != text:
        page.write_text(new)
        print(f"include-examples: updated {page.relative_to(root)}", file=sys.stderr)
