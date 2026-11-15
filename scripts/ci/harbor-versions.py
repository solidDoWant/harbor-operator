#!/usr/bin/env python3
"""Reads test/harbor/versions.yaml, the Harbor minor releases the e2e suite
runs against.

  harbor-versions.py matrix            # JSON list of minors, for a CI matrix
  harbor-versions.py get MINOR KEY     # one value of the entry for MINOR
  harbor-versions.py get "" KEY        # the same for the first entry

The file is a flat list of entries with `key: value` lines, so this needs no
YAML library.
"""

import json
import pathlib
import sys

DEFAULT = pathlib.Path(__file__).resolve().parents[2] / "test/harbor/versions.yaml"


def load(path=DEFAULT):
    entries = []
    for raw in pathlib.Path(path).read_text().splitlines():
        line = raw.split("#", 1)[0].rstrip() if raw.lstrip().startswith("#") else raw.rstrip()
        if not line.strip():
            continue
        if line.startswith("- "):
            entries.append({})
            line = line[2:]
        key, _, value = line.strip().partition(":")
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] == '"':
            value = value[1:-1]
        entries[-1][key.strip()] = value
    return entries


def find(entries, minor):
    if not entries:
        raise SystemExit("no Harbor versions listed")
    if minor == "":
        return entries[0]
    for entry in entries:
        if entry["minor"] == minor:
            return entry
    raise SystemExit(f"Harbor minor {minor} is not listed in test/harbor/versions.yaml")


def main(argv):
    entries = load()
    if argv[:1] == ["matrix"]:
        print(json.dumps([entry["minor"] for entry in entries]))
    elif argv[:1] == ["get"] and len(argv) == 3:
        print(find(entries, argv[1])[argv[2]])
    else:
        raise SystemExit(__doc__)


if __name__ == "__main__":
    main(sys.argv[1:])
