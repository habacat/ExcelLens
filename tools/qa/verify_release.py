"""Verify ExcelLens release archives and optionally assemble the tagged source and checksum manifest."""
import argparse
import hashlib
import json
import os
import re
import subprocess
import zipfile
from pathlib import Path
from tempfile import TemporaryDirectory

ROOT = Path(__file__).resolve().parents[2]


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def write_json(path, data):
    path.write_bytes((json.dumps(data, ensure_ascii=False, indent=2) + "\n").encode("utf-8"))


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


def source_tree(ref):
    commit = git("rev-parse", "--verify", "--end-of-options", ref + "^{commit}").decode().strip()
    algorithm = git("rev-parse", "--show-object-format").decode().strip()
    files = {}
    for line in git("ls-tree", "-rz", commit).split(b"\0"):
        if not line:
            continue
        meta, name = line.split(b"\t", 1)
        mode, kind, oid = meta.split()
        require(kind == b"blob" and mode in (b"100644", b"100755"), "Unexpected Git entry: " + repr(name))
        files[name.decode("utf-8")] = oid.decode()
    return commit, algorithm, files


def archive_names(z):
    names = z.namelist()
    require(len(names) == len(set(names)), "Duplicate ZIP paths")
    for name in names:
        parts = name.rstrip("/").split("/")
        require(not {"", ".", ".."}.intersection(parts) and "\\" not in name and ":" not in parts[0],
                "Invalid archive path: " + name)
        require(parts[0] != "fortest" and "private" not in parts, "Private fixture in archive: " + name)
    require(z.testzip() is None, "ZIP failed CRC verification")
    return {n for n in names if not n.endswith("/")}


def git_blob(z, name, algorithm):
    size = z.getinfo(name).file_size
    h = hashlib.new(algorithm, b"blob " + str(size).encode("ascii") + b"\0")
    with z.open(name) as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def verify_source(archive, version, tree):
    commit, algorithm, files = tree
    with zipfile.ZipFile(archive) as z:
        require(archive_names(z) == set(files), "Source ZIP paths differ from the Git tree")
        for name, oid in files.items():
            require(git_blob(z, name, algorithm) == oid, "Source file differs from Git: " + name)
    return {"version": version, "result": "PASS", "commit": commit, "filesVerified": len(files),
            "zipSHA256": digest(archive), "checks": ["ZIP CRC", "exact Git file set", "every Git blob", "private fixtures excluded"]}


def manifest_for(version, commit=None):
    # The maintenance checkout and the public distribution may ship different
    # documentation. Prefer the official tag, including when checking a local
    # download. An unpacked source tree or a pre-tag build uses its own list.
    if commit is None and (ROOT / ".git").exists():
        tag = "refs/tags/v" + version + "^{commit}"
        found = subprocess.run(["git", "rev-parse", "--quiet", "--verify", tag], cwd=ROOT,
                               stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        if found.returncode == 0:
            commit = found.stdout.decode().strip()
    if commit:
        return json.loads(git("show", commit + ":tools/release-files.json"))
    return json.loads((ROOT / "tools/release-files.json").read_text(encoding="utf-8"))


def verify_windows(archive, version, tree=None, *, candidate=False):
    directory = archive.parent
    entries = (json.loads((ROOT / "tools/release-files.json").read_text(encoding="utf-8"))
               if candidate and not tree else manifest_for(version, tree[0] if tree else None))
    sources = {s.replace("{version}", version): s.replace("{version}", version) for s in entries}
    sources.update({"ExcelLens_icon.svg":"internal/branding/icon.svg", "ExcelLens_icon.ico":"internal/branding/icon.ico"})
    expected = set(sources) | {"ExcelLens.exe", "SHA256SUMS.txt", "BUILD_INFO.txt"}
    with zipfile.ZipFile(archive) as z:
        require(archive_names(z) == expected, "Windows ZIP paths differ from the release manifest")
        for name in expected:
            with z.open(name) as entry, (directory / name).open("rb") as local:
                while True:
                    block = entry.read(1024 * 1024)
                    require(block == local.read(len(block) or 1), "Release directory differs from ZIP: " + name)
                    if not block:
                        break
        if tree is not None:
            _, algorithm, files = tree
            for name, path in sources.items():
                require(git_blob(z, name, algorithm) == files.get(path), "Packaged resource differs from release commit: " + name)
        size = z.getinfo("ExcelLens.exe").file_size
        executable = directory / "ExcelLens.exe"
        with executable.open("rb") as stream:
            require(stream.read(2) == b"MZ", "Executable is not Windows PE")
        exe_hash = digest(executable)
        require(z.read("SHA256SUMS.txt").decode("ascii").split()[0] == exe_hash, "Executable checksum mismatch")
        require(z.read("BUILD_INFO.txt").decode("utf-8-sig").startswith("ExcelLens " + version + "\r\n"), "Build version mismatch")
    return {"version": version, "result": "PASS", "packagedFilesVerified": len(expected),
            "exeBytes": size, "exeSHA256": exe_hash, "zipSHA256": digest(archive),
            "checks": ["ZIP CRC", "every file equals the release directory", "EXE SHA-256 and PE header", "version"]}


def finalize(directory, version, ref, performance_report=None):
    tree = source_tree(ref)
    windows = directory / ("ExcelLens_" + version + "_Windows_x64_Full.zip")
    win = verify_windows(windows, version, tree)
    # The checksum manifest is promoted last. Failed builds never publish
    # unchecked archives, and rerunning can complete an interrupted promotion.
    with TemporaryDirectory(prefix=".source-", dir=str(directory)) as tmp:
        staging = Path(tmp)
        archive = staging / ("ExcelLens_" + version + "_Source_Full.zip")
        git("archive", "--format=zip", "-9", "--output=" + str(archive), tree[0])
        src = verify_source(archive, version, tree)
        write_json(staging / "source-package.json", src)
        sums = staging / ("ExcelLens_" + version + "_SHA256SUMS.txt")
        hashes = {windows.name: win["zipSHA256"], archive.name: src["zipSHA256"]}
        products = [archive, staging / "source-package.json"]
        if performance_report is not None:
            report = json.loads(performance_report.read_bytes())
            require(report["version"] == version and report["checkoutHead"] == tree[0]
                    and report["modifiedBuildInputs"] is False and report["profile"] == "release"
                    and report["result"] == "PASS" and report["sourceDigestsVerified"] is True,
                    "Performance report must come from the tested release checkout")
            report["cleanup"] = [{k: entry[k] for k in ("scenario", "code")} for entry in report["cleanup"]]
            public_report = staging / ("ExcelLens_" + version + "_Performance.json")
            write_json(public_report, report)
            products.append(public_report)
            hashes[public_report.name] = digest(public_report)
        sums.write_bytes("".join(value + "  " + name + "\n" for name, value in hashes.items()).encode("ascii"))
        for item in [*products, sums]:
            os.replace(str(item), str(directory / item.name))
    write_json(ROOT / "validation" / version / "release-package.json", win)
    return {"result":"PASS", "version":version, "windows":win, "source":src, "checksums":sums.name}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version")
    parser.add_argument("--dir", type=Path, default=ROOT / "release")
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--source", action="store_true")
    modes.add_argument("--candidate", action="store_true", help="Verify a StageOnly package against the development manifest")
    modes.add_argument("--finalize", action="store_true", help="Create and verify the tagged source ZIP and release checksums")
    parser.add_argument("--ref", help="Git tag or commit to verify")
    parser.add_argument("--performance-report", type=Path, help="Include the completed release matrix when finalizing")
    args = parser.parse_args()
    version, directory = args.version, args.dir.resolve()
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        parser.error("Expected a numeric release version")
    if args.performance_report and not args.finalize:
        parser.error("--performance-report requires --finalize")
    if args.finalize:
        result = finalize(directory, version, args.ref or "v" + version, args.performance_report)
    elif args.source:
        result = verify_source(directory / ("ExcelLens_" + version + "_Source_Full.zip"), version, source_tree(args.ref or "HEAD"))
        write_json(directory / "source-package.json", result)
    else:
        tree = source_tree(args.ref) if args.ref else None
        result = verify_windows(directory / ("ExcelLens_" + version + "_Windows_x64_Full.zip"), version, tree, candidate=args.candidate)
        receipt = directory / "candidate-package.json" if args.candidate else ROOT / "validation" / version / "release-package.json"
        write_json(receipt, result)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
