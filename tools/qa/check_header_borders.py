"""Check merged header border continuity in actual browser screenshots."""
import json
import math
import sys
from pathlib import Path
from PIL import Image

directory = Path(sys.argv[1]).resolve()
scenarios = json.loads((directory / "private" / "border-scenarios.json").read_text(encoding="utf-8"))
results = []
for scenario in scenarios:
    image = Image.open(directory / "private" / scenario["file"]).convert("RGB")
    scale = image.width / scenario["viewportWidth"]
    for edge in scenario["edges"]:
        x = round(edge["x"] * scale)
        first = math.ceil(edge["y"] * scale) + 2
        last = math.floor((edge["y"] + edge["height"]) * scale) - 2
        if first < 0 or last >= image.height or x < 3 or x + 2 >= image.width:
            raise AssertionError("Border is outside the captured viewport")
        missing = []
        for y in range(first, last + 1):
            if not any(max(image.getpixel((column, y))) < 180 for column in range(x - 3, x + 3)):
                missing.append(y - first)
        results.append({"mode": scenario["mode"], "dpr": scenario["dpr"], "zoom": scenario["zoom"], "ref": edge["ref"], "checkedPixels": last - first + 1, "missingPixels": len(missing), "missingOffsets": missing})
report = {"result": "PASS" if all(r["missingPixels"] == 0 for r in results) else "FAIL", "scenarios": len(scenarios), "edges": len(results), "checkedPixels": sum(r["checkedPixels"] for r in results), "missingPixels": sum(r["missingPixels"] for r in results), "checks": results}
(directory / "header-borders.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
print(json.dumps({k: v for k, v in report.items() if k != "checks"}, ensure_ascii=False))
if report["result"] != "PASS":
    raise SystemExit(1)
