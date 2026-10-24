#!/usr/bin/env python3
"""Prints the tool calls of an eval run's transcript: `evals/show.py <run dir or transcript.jsonl>`."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from run import parse_transcript  # noqa: E402

path = Path(sys.argv[1])
if path.is_dir():
    path = path / "transcript.jsonl"
width = int(sys.argv[2]) if len(sys.argv) > 2 else 400
calls, final = parse_transcript(path.read_text())
for i, c in enumerate(calls, 1):
    inp = c["input"]
    what = inp.get("command") or inp.get("file_path") or inp.get("skill") or inp.get("pattern") or str(inp)
    print(f"--- {i}. {c['tool']}{' (error)' if c['error'] else ''}: {what}")
    if c["tool"] in ("Write", "Edit"):
        print((inp.get("content") or inp.get("new_string") or "")[:width])
    out = c["output"].strip()
    if out:
        print("  > " + out[:width].replace("\n", "\n  > "))
print(f"--- result: {final.get('subtype')}, {final.get('num_turns')} turns, ${final.get('total_cost_usd') or 0:.2f}")
print((final.get("result") or "")[:width])
