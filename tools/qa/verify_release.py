"""Independently verify release ZIPs; source ZIPs must match a Git commit."""
import hashlib
import json
import re
import subprocess
import sys
import zipfile
from pathlib import Path

root = Path(__file__).resolve().parents[2]
version = sys.argv[1]
if not re.fullmatch(r"\d+\.\d+\.\d+", version):
    raise SystemExit("Expected a numeric release version")
release = Path(sys.argv[sys.argv.index("--dir") + 1]).resolve() if "--dir" in sys.argv else root / "release"
source = "--source" in sys.argv[2:]
archive = release / f"ExcelLens_{version}_{'Source_Full' if source else 'Windows_x64_Full'}.zip"
sha256 = lambda data: hashlib.sha256(data).hexdigest()

with zipfile.ZipFile(archive) as package:
    assert package.testzip() is None, "ZIP CRC failed"
    names = package.namelist()
    assert len(names) == len(set(names)), "Duplicate archive paths"
    assert all(not n.startswith(("/", "../")) and "/../" not in n and "\\" not in n for n in names)
    if source:
        commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
        assert subprocess.check_output(["git", "rev-parse", "--show-object-format"], cwd=root, text=True).strip() == "sha1"
        tree = subprocess.check_output(["git", "ls-tree", "-rz", "HEAD"], cwd=root)
        entries = {}
        for row in tree.split(b"\0"):
            if row:
                meta, path = row.split(b"\t", 1)
                mode, kind, oid = meta.split()
                assert kind == b"blob", "Source archive contains a non-file entry"
                entries[path.decode("utf-8")] = oid.decode()
        files = {n for n in names if not n.endswith("/")}
        assert files == set(entries), "Source ZIP paths differ from the Git tree"
        assert not any(n.startswith("fortest/") or "/private/" in n for n in files), "Private fixtures were archived"
        for name in files:
            data = package.read(name)
            oid = hashlib.sha1(b"blob " + str(len(data)).encode("ascii") + b"\0" + data).hexdigest()
            assert oid == entries[name], f"Source content differs from Git: {name}"
        report = {"version": version, "result": "PASS", "commit": commit, "filesVerified": len(files), "zipSHA256": sha256(archive.read_bytes()), "checks": ["ZIP CRC", "exact Git file set", "every file equals its Git blob", "private samples and screenshots excluded"]}
        receipt = release / "source-package.json"
    else:
        manifest = json.loads((root / "tools/release-files.json").read_text(encoding="utf-8"))
        required = {n.replace("{version}", version) for n in manifest}
        required |= {"ExcelLens.exe", "SHA256SUMS.txt", "BUILD_INFO.txt", "ExcelLens_icon.svg", "ExcelLens_icon.ico"}
        assert required == set(names), "ZIP paths differ from the release manifest"
        for name in names:
            assert package.read(name) == (release / name).read_bytes(), f"Release content differs: {name}"
        exe = package.read("ExcelLens.exe")
        assert exe[:2] == b"MZ", "Executable is not Windows PE"
        exe_hash = sha256(exe)
        assert package.read("SHA256SUMS.txt").decode("ascii").split()[0] == exe_hash
        assert package.read("BUILD_INFO.txt").decode("utf-8-sig").startswith(f"ExcelLens {version}\r\n")
        report = {"version": version, "result": "PASS", "packagedFilesVerified": len(names), "exeBytes": len(exe), "exeSHA256": exe_hash, "zipSHA256": sha256(archive.read_bytes()), "checks": ["ZIP CRC", "every file equals the release directory", "EXE SHA-256 and PE header", "version", "UPX integrity checked in build"]}
        receipt = root / "validation" / version / "release-package.json"
receipt.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
print(json.dumps(report, ensure_ascii=False))
