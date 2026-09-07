#!/usr/bin/env python3
"""Portable source/annotation policy checks; LuaLS owns semantic type checking."""
import ast
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def annotation_errors(text, require_signatures=False):
    errors = []
    lines = text.splitlines()
    for index, line in enumerate(lines):
        if re.match(r"\s*---@", line):
            if re.search(r"\b(any|unknown)\b|\btable\b(?!\s*<)|\bfunction\b", line):
                errors.append(f"line {index + 1}: annotation escape hatch")
            if re.match(r"\s*---@diagnostic\s+disable", line):
                errors.append(f"line {index + 1}: diagnostic suppression")
        match = re.match(r"^function\s+[\w.:]+\(([^)]*)\)", line)
        if not require_signatures or not match:
            continue
        start = index
        while start and lines[start - 1].lstrip().startswith("---"):
            start -= 1
        annotations = "\n".join(lines[start:index])
        for parameter in filter(None, (name.strip() for name in match[1].split(","))):
            pattern = r"---@param\s+" + re.escape(parameter) + r"\??\s+\S+"
            if not re.search(pattern, annotations):
                errors.append(f"line {index + 1}: missing parameter type for {parameter}")
        if not re.search(r"---@return\s+\S+", annotations):
            errors.append(f"line {index + 1}: missing return type")
    return errors


def main():
    contract = json.loads((ROOT / "api-contracts.json").read_text())
    required = set(contract["annotated_modules"])
    errors = []
    seen = set()
    for directory in ("lua", "plugin", "tests", "scripts", "runtime", "integrations"):
        base = ROOT / directory
        if not base.exists():
            continue
        for path in sorted(base.rglob("*")):
            if "__pycache__" in path.parts:
                continue
            if path.is_symlink():
                errors.append(str(path.relative_to(ROOT)) + ": source symlink is not allowed")
                continue
            if not path.is_file():
                continue
            relative = path.relative_to(ROOT).as_posix()
            text = path.read_text()
            if path.suffix == ".py":
                ast.parse(text, filename=relative)
            if text.startswith("#!/bin/sh"):
                subprocess.run(["sh", "-n", str(path)], check=True)
            if path.suffix == ".lua":
                seen.add(relative)
                errors.extend(relative + ": " + error for error in
                              annotation_errors(text, relative in required))
                if relative.startswith("lua/") and re.search(r'require\(["\x27]config\.', text):
                    errors.append(relative + ": personal configuration dependency")
    errors.extend("missing annotated module: " + name for name in sorted(required - seen))
    if errors:
        raise SystemExit("\n".join(errors))
    print("Python/shell syntax and annotation policy: passed (semantic types require LuaLS)")


if __name__ == "__main__":
    main()
