#!/usr/bin/env python3
"""Install the pinned, verified k6 binary locally on macOS arm64.

Only the official v2.3.0 assets below are accepted. Existing archives and
binaries are verified again; no downloaded executable is run by this script.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import stat
import tempfile
import time
import urllib.request
import zipfile


VERSION = "v2.3.0"
ASSET = "k6-v2.3.0-macos-arm64.zip"
CHECKSUM_ASSET = "k6-v2.3.0-checksums.txt"
ENTRY = "k6-v2.3.0-macos-arm64/k6"
ARCHIVE_SHA256 = "b2417a3038edc5fe81dc178a889237724b595c5c9cfed875822008e46e862c7d"
RELEASE_URL = "https://github.com/grafana/k6/releases/download/v2.3.0/"
ROOT = Path(__file__).resolve().parents[2]
DESTINATION = ROOT / ".local" / "tools" / "k6-v2.3.0"
CHUNK = 1024 * 1024


def digest_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(CHUNK), b""):
            digest.update(block)
    return digest.hexdigest()


def regular_or_missing(path):
    try:
        mode = path.lstat().st_mode
    except FileNotFoundError:
        return False
    if not stat.S_ISREG(mode):
        raise ValueError(f"refusing non-regular cache/output file: {path}")
    return True


def atomic_write(path, data, mode=0o600):
    regular_or_missing(path)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".k6-install-", delete=False) as handle:
            temporary = Path(handle.name)
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
            os.fchmod(handle.fileno(), mode)
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def download(asset, limit):
    request = urllib.request.Request(RELEASE_URL + asset, headers={"User-Agent": "grpctest-k6-installer"})
    deadline = time.monotonic() + 120
    data = bytearray()
    with urllib.request.urlopen(request, timeout=30) as response:
        if response.status != 200 or not response.geturl().startswith("https://"):
            raise ValueError("official release download did not return HTTPS 200")
        while True:
            if time.monotonic() > deadline:
                raise TimeoutError("official release download exceeded 120 seconds")
            block = response.read(min(CHUNK, limit + 1 - len(data)))
            if not block:
                break
            data.extend(block)
            if len(data) > limit:
                raise ValueError("official release download exceeded its size limit")
    return bytes(data)


def verify_checksums(data):
    if len(data) > CHUNK:
        raise ValueError("checksum file exceeds its size limit")
    entries = {}
    for line in data.decode("ascii").splitlines():
        if not line.strip():
            continue
        match = re.fullmatch(r"([0-9a-f]{64})\s+\*?([^\s/\\]+)", line)
        if match is None or match[2] in entries:
            raise ValueError("invalid or duplicate official checksum entry")
        entries[match[2]] = match[1]
    if entries.get(ASSET) != ARCHIVE_SHA256:
        raise ValueError("official checksum does not match the independently pinned archive SHA256")


def verified_binary(archive_path):
    if archive_path.stat().st_size > 64 * CHUNK or digest_file(archive_path) != ARCHIVE_SHA256:
        raise ValueError("k6 archive SHA256 mismatch; refusing to install or execute it")
    with zipfile.ZipFile(archive_path) as archive:
        files = archive.infolist()
        names = [info.filename for info in files]
        # Never extract ZIP paths. Read only this one fixed file after the entire
        # archive has matched the pinned official checksum.
        if len(set(names)) != len(names) or set(names) != {ENTRY, ENTRY.rsplit("/", 1)[0] + "/"}:
            raise ValueError("unexpected archive entries")
        info = archive.getinfo(ENTRY)
        if not stat.S_ISREG(info.external_attr >> 16) or not 0 < info.file_size <= 128 * CHUNK:
            raise ValueError("expected k6 archive entry is not a bounded regular file")
        binary = archive.read(info)
        if len(binary) != info.file_size:
            raise ValueError("truncated k6 archive entry")
    return binary


def install():
    if platform.system() != "Darwin" or platform.machine().lower() != "arm64":
        raise ValueError("this pinned installer supports only macOS (darwin) arm64")
    for directory in (ROOT / ".local", ROOT / ".local" / "tools", DESTINATION):
        if directory.is_symlink():
            raise ValueError(f"refusing symlink installation directory: {directory}")
        directory.mkdir(mode=0o700, exist_ok=True)
    checksum_path = DESTINATION / "checksums.txt"
    checksum = checksum_path.read_bytes() if regular_or_missing(checksum_path) else download(CHECKSUM_ASSET, CHUNK)
    verify_checksums(checksum)
    if not checksum_path.exists():
        atomic_write(checksum_path, checksum)
    archive_path = DESTINATION / "k6.zip"
    if not regular_or_missing(archive_path):
        archive = download(ASSET, 64 * CHUNK)
        if hashlib.sha256(archive).hexdigest() != ARCHIVE_SHA256:
            raise ValueError("downloaded archive SHA256 mismatch; refusing installation")
        atomic_write(archive_path, archive)
    binary = verified_binary(archive_path)
    binary_hash = hashlib.sha256(binary).hexdigest()
    executable = DESTINATION / "k6"
    unchanged = regular_or_missing(executable) and digest_file(executable) == binary_hash
    if not unchanged:
        atomic_write(executable, binary, mode=0o755)
    else:
        executable.chmod(0o755)
    record = {
        "version": VERSION,
        "platform": "darwin/arm64",
        "archive_url": RELEASE_URL + ASSET,
        "checksums_url": RELEASE_URL + CHECKSUM_ASSET,
        "archive_sha256": ARCHIVE_SHA256,
        "checksums_sha256": hashlib.sha256(checksum).hexdigest(),
        "binary_entry": ENTRY,
        "binary_sha256": binary_hash,
    }
    atomic_write(DESTINATION / "verified.json", (json.dumps(record, indent=2) + "\n").encode())
    return {"path": str(executable), "version": VERSION, "binary_sha256": binary_hash,
            "status": "verified_existing" if unchanged else "installed_verified"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.parse_args()
    try:
        result = install()
    except (OSError, ValueError, zipfile.BadZipFile, RuntimeError) as error:
        parser.exit(1, f"k6 installation refused: {error}\n")
    print(json.dumps(result))


if __name__ == "__main__":
    main()
