"""Pair prebuilt Go test binaries on the same host, alternating execution order."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import statistics
import subprocess

BENCHMARK = re.compile(
    r"^(Benchmark\S+)\s+\d+\s+([\d.]+) ns/op\b.*?\s+([\d.]+) B/op\s+([\d.]+) allocs/op"
)


def sample(binary, args, env):
    command = [str(binary), "-test.run=^$", "-test.bench=" + args.bench,
               "-test.benchtime=" + args.benchtime, "-test.benchmem"]
    output = subprocess.run(command, env=env, capture_output=True, text=True,
                            encoding="utf-8", errors="replace", timeout=180, check=True).stdout
    rows = {}
    for line in output.splitlines():
        match = BENCHMARK.match(line)
        if match:
            rows[match[1]] = [float(match[2]), int(match[3]), int(match[4])]
    if not rows or "PASS" not in output.splitlines():
        raise ValueError("missing measurement or failed assertion: " + output)
    return rows, [line for line in output.splitlines() if line.startswith(("cpu:", "goos:", "goarch:"))]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("before", type=Path)
    parser.add_argument("after", type=Path)
    parser.add_argument("--bench", required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--pairs", type=int, default=8)
    parser.add_argument("--benchtime", default="500ms")
    parser.add_argument("--procs", type=int, default=4)
    args = parser.parse_args()
    if args.pairs < 2 or args.procs < 1:
        parser.error("need at least two pairs and one CPU")
    binaries = {"before": args.before.resolve(), "after": args.after.resolve()}
    env = dict(os.environ, GOMAXPROCS=str(args.procs))
    report = {"benchmark": args.bench, "benchtime": args.benchtime, "GOMAXPROCS": args.procs,
              "GODEBUG": env.get("GODEBUG", ""), "units": ["ns/op", "B/op", "allocs/op"],
              "binaries": {}, "pairs": []}
    for key, binary in binaries.items():
        digest = hashlib.sha256()
        with binary.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1 << 20), b""):
                digest.update(chunk)
        report["binaries"][key] = {"name": binary.name, "sha256": digest.hexdigest()}
    for i in range(args.pairs):
        order = ["before", "after"] if i % 2 == 0 else ["after", "before"]
        pair = {"order": order}
        for label in order:
            pair[label], host = sample(binaries[label], args, env)
            if report.setdefault("host", host) != host:
                raise ValueError("benchmark host changed")
        if pair["before"].keys() != pair["after"].keys():
            raise ValueError("benchmark names do not match")
        report["pairs"].append(pair)
    report["summary"] = {}
    for name in report["pairs"][0]["before"]:
        before = [p["before"][name] for p in report["pairs"]]
        after = [p["after"][name] for p in report["pairs"]]
        ratios = [a[0] / b[0] for b, a in zip(before, after)]
        report["summary"][name] = {
            "medianBefore": list(map(statistics.median, zip(*before))),
            "medianAfter": list(map(statistics.median, zip(*after))),
            "medianPairedTimeChangePct": round(100 * (statistics.median(ratios) - 1), 2),
            "fasterPairs": sum(r < 1 for r in ratios),
        }
    args.out.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report["summary"], ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()
