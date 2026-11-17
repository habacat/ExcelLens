"""Independent, read-only evidence for a supplied Oracle TXT in truncated TAR.

Uses Python's tar header parser / GB18030 decoder, never ExcelLens code.
Emits metadata and digests only; no account names or individual ledger values.
"""
import argparse
import hashlib
import json
import tarfile
from pathlib import Path


def inspect(path):
    size = path.stat().st_size
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    with path.open("rb") as stream:
        header = tarfile.TarInfo.frombuf(stream.read(512), "utf-8", "surrogateescape")
        raw_digest = hashlib.sha256()
        complete_digest = hashlib.sha256()
        canonical = hashlib.sha256()
        rows, physical, complete_bytes = 0, 0, 0
        tail_bytes = 0
        for raw in stream:
            raw_digest.update(raw)
            if not raw.endswith(b"\n"):
                tail_bytes = len(raw)
                break
            physical += 1
            complete_bytes += len(raw)
            complete_digest.update(raw)
            line = raw[:-1].decode("gb18030")
            if line.endswith("\r"):
                line = line[:-1]
            fields = line.split("@")
            if physical == 1:
                assert len(fields) == 77
                head = fields[:39]
                assert head[38].startswith("seg12_desc")
                head[38] = "seg12_desc"
                canonical.update(("@".join(head) + "\n").encode())
                rows += 1
                fields = fields[38:]
                fields[0] = fields[0][len("seg12_desc"):]
            assert len(fields) == 39, (physical, len(fields))
            canonical.update(("@".join(fields) + "\n").encode())
            rows += 1
    return dict(source_bytes=size, source_sha256=digest.hexdigest(),
                member=header.name, declared_bytes=header.size,
                available_bytes=size - 512, missing_bytes=header.size - (size - 512),
                raw_fragment_sha256=raw_digest.hexdigest(),
                complete_prefix_bytes=complete_bytes, omitted_tail_bytes=tail_bytes,
                complete_prefix_sha256=complete_digest.hexdigest(),
                physical_complete_lines=physical, recovered_rows=rows, columns=39,
                canonical_sha256=canonical.hexdigest())


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("path", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    evidence = json.dumps(inspect(args.path), ensure_ascii=False, indent=2) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(evidence, encoding="utf-8")
    print(evidence, end="")
