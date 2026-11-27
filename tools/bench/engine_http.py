"""Measure the real loopback upload, query jobs and filtered read in fresh uiprobe processes."""

import argparse
from decimal import Decimal
import hashlib
import json
import os
from pathlib import Path
import re
import statistics
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlencode, urlsplit
from urllib.request import build_opener, ProxyHandler, Request


def ledger(rows):
    data = bytearray(b"id,group,batch,amount,memo\r\n")
    for i in range(rows):
        memo = "审计" if i == rows - 1 else '费用报销,Ledger ""entry""'
        record = f'{i:08},Group{i % 4},Batch{i // 4 % 2},9007199254740993.125,"{memo}"\r\n'
        data.extend(record.encode())
    return bytes(data)


def check_aggregates(job, sheet, rows):
    measures = ["sum", "average", "min", "max", "count", "distinct"]
    query = {"source": "wb:" + sheet, "headerRow": 1, "rows": ["C2"], "columns": ["C3"],
             "metrics": [{"field": "C4", "aggregate": op} for op in measures], "precision": 3}
    result = job("analysis/query", query)["result"]
    if result["included"] != rows or result["columnKeys"] != ["Batch0", "Batch1"]:
        raise ValueError("incorrect cross-tab dimensions")
    amount = Decimal("9007199254740993.125")

    def check(cells, count):
        expected = [amount * count, amount, amount, amount, Decimal(count), Decimal(1)]
        if [Decimal(cell["exact"]) for cell in cells] != expected or any(cell["count"] != count for cell in cells):
            raise ValueError("incorrect exact aggregate or count")

    check(result["metricGrand"], rows)
    for total in result["metricColumnTotals"]:
        check(total, rows // 2)
    if {tuple(row["keys"]) for row in result["rows"]} != {(f"Group{i}",) for i in range(4)}:
        raise ValueError("incorrect cross-tab groups")
    for row in result["rows"]:
        check(row["metricTotals"], rows // 4)
        if len(row["metricValues"]) != 2:
            raise ValueError("missing cross-tab bucket")
        for bucket in row["metricValues"]:
            check(bucket, rows // 8)


def measure(probe, source, rows, root, aggregate=False):
    hidden = getattr(subprocess, "CREATE_NO_WINDOW", 0)
    with subprocess.Popen([str(probe), "-duration", "4m", "-storage", root], stdout=subprocess.PIPE,
                          creationflags=hidden, env=dict(os.environ, GOMAXPROCS="4")) as child:
        try:
            base = json.loads(child.stdout.readline())["url"].rstrip("/")
            if urlsplit(base).hostname != "127.0.0.1":
                raise RuntimeError("expected a loopback probe")
            http = build_opener(ProxyHandler({}))
            with http.open(base, timeout=30) as response:
                token = re.search(r'<meta name="app-token" content="([a-z0-9]+)"', response.read().decode())[1]

            def api(route, data=None, content_type="application/json"):
                if isinstance(data, dict):
                    data = json.dumps(data).encode()
                headers = {"X-ExcelLens-Token": token, "Content-Type": content_type}
                with http.open(Request(base + route, data=data, headers=headers), timeout=120) as response:
                    return json.load(response)

            def job(kind, payload):
                started = api(f"/api/{kind}/start", payload)
                if started.get("ready"):
                    return started
                route = f"/api/{kind}/status?" + urlencode({"id": started["jobId"]})
                deadline = time.monotonic() + 120
                while time.monotonic() < deadline:
                    status = api(route)
                    if status["status"] == "done":
                        return status
                    if status["status"] in ("cancelled", "error"):
                        raise RuntimeError(status.get("error", status["status"]))
                    time.sleep(.005)
                raise TimeoutError(kind)

            boundary = "excellens-ledger-benchmark"
            upload = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="ledger.csv"\r\n\r\n'.encode()
                      + source + f'\r\n--{boundary}--\r\n'.encode())
            start = time.perf_counter()
            book = api("/api/open", upload, "multipart/form-data; boundary=" + boundary)
            sheet = book["sheets"][0]["name"]
            request = {"sheet": sheet, "workbookKey": book["workbookKey"]}
            job("index", {"sheet": sheet})
            indexed = time.perf_counter()
            found = job("search", dict(request, query="审计", scope="current", mode="value"))
            if [hit["ref"] for hit in found["matches"]] != [f"E{rows + 1}"]:
                raise ValueError("incorrect search hits")
            searched = time.perf_counter()
            ends = []
            for group in (1, 2):
                rules = [{"column": 2, "operator": "equals", "value": f"group{group}"},
                         {"column": 3, "operator": "in", "values": ["BATCH1"]}]
                result = job("filter", dict(request, rules=rules, headerRow=1))
                if result["matchedRows"] != rows // 8 + 1:
                    raise ValueError("incorrect filter count")
                for offset, index in ((1, 4 + group), (rows // 8, rows - 4 + group)):
                    page = api("/api/sheet?" + urlencode(dict(request, filterId=result["filterId"],
                                                              offset=offset, limit=1, col=1, cols=5)))
                    row, = page["rows"]
                    if (row["number"], row["cells"][0]["raw"], row["cells"][3]["raw"]) != (
                            index + 2, f"{index:08}", "9007199254740993.125"):
                        raise ValueError("incorrect filtered source values")
                ends.append(time.perf_counter())
            extra = {}
            if aggregate:
                check_aggregates(job, sheet, rows)
                finished = time.perf_counter()
                extra = {"aggregateMs": (finished - ends[1]) * 1000, "includingAggregateMs": (finished - start) * 1000}
            command = [sys.executable, str(Path(__file__).with_name("metrics.py")), str(child.pid), root]
            meter = subprocess.run(command, input='{"action":"sample"}\n', capture_output=True, text=True,
                                   check=True, timeout=15, creationflags=hidden)
            peak = json.loads(meter.stdout.splitlines()[-1])["current"]["processPeakRSSBytes"]
            return dict(totalMs=(ends[1]-start)*1000, openIndexMs=(indexed-start)*1000,
                        searchMs=(searched-indexed)*1000, coldFilterReadMs=(ends[0]-searched)*1000,
                        warmFilterReadMs=(ends[1]-ends[0])*1000, processPeakRSSBytes=peak, **extra)
        finally:
            child.terminate()
            child.wait(timeout=10)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("before", type=Path)
    parser.add_argument("after", type=Path)
    parser.add_argument("--rows", type=int, default=100000)
    parser.add_argument("--pairs", type=int, default=8)
    parser.add_argument("--aggregate", action="store_true", help="also check six exact measures across all ledger groups and totals")
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    if args.rows < 8 or args.rows % 8 or args.pairs < 1:
        parser.error("use a positive multiple of eight rows and at least one pair")
    source = ledger(args.rows)
    probes = {"before": args.before.resolve(), "after": args.after.resolve()}
    report = {"rows": args.rows, "sourceBytes": len(source), "sourceSHA256": hashlib.sha256(source).hexdigest(),
              "probeSHA256": {k: hashlib.sha256(v.read_bytes()).hexdigest() for k, v in probes.items()},
              "GOMAXPROCS": 4, "pollMs": 5, "pairs": []}
    if args.aggregate:
        report["aggregate"] = {"groups": 4, "columns": 2, "metrics": ["sum", "average", "min", "max", "count", "distinct"]}
    for i in range(args.pairs):
        order = ["before", "after"] if i % 2 == 0 else ["after", "before"]
        pair = {"order": order}
        for key in order:
            with tempfile.TemporaryDirectory(prefix="excellens-ui-qa-") as root:
                pair[key] = measure(probes[key], source, args.rows, root, args.aggregate)
        report["pairs"].append(pair)
    summary = {}
    for metric in report["pairs"][0]["before"]:
        values = [(p["before"][metric], p["after"][metric]) for p in report["pairs"]]
        ratios = [a/b for b, a in values]
        summary[metric] = dict(medianBefore=statistics.median(v[0] for v in values),
                               medianAfter=statistics.median(v[1] for v in values),
                               medianPairedChangePct=round((statistics.median(ratios)-1)*100, 2),
                               lowerPairs=sum(r < 1 for r in ratios))
    report["summary"] = summary
    args.out.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(summary), flush=True)


if __name__ == "__main__":
    main()
