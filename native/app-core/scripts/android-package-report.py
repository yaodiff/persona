#!/usr/bin/env python3
"""Report and verify native Android library size and APK packaging; no binaries are committed."""
import argparse
import csv
import gzip
import hashlib
import json
import os
import re
import struct
import subprocess
import sys
import zipfile
from pathlib import Path

ABIS = ("arm64-v8a", "x86_64")
ALIGNMENT = 16 * 1024


def inspect_elf(data, abi):
    """Read the packaged ELF itself: correct 64-bit little-endian machine and every LOAD's page alignment."""
    if data[:8] != b'\x7fELF\x02\x01\x01\x00':
        raise ValueError(abi + ': expected a 64-bit little-endian ELF library')
    endian, header = '<', struct.unpack_from('<HHIQQQIHHHHHH', data, 16)
    machine, phoff, phentsize, phnum = header[1], header[4], header[8], header[9]
    expected = {'arm64-v8a': 183, 'x86_64': 62}[abi]
    if machine != expected:
        raise ValueError(f'{abi}: wrong ELF machine {machine}, expected {expected}')
    if len(data) < 64 or phoff + phentsize * phnum > len(data):
        raise ValueError(abi + ': truncated ELF header or program headers')
    loads = []
    for index in range(phnum):
        offset = phoff + index * phentsize
        kind, _, _, _, _, _, _, align = struct.unpack_from(endian + 'IIQQQQQQ', data, offset)
        if kind == 1:
            loads.append(align)
    if not loads or any(align != ALIGNMENT for align in loads):
        raise ValueError(f'{abi}: LOAD alignments {loads} are not all {ALIGNMENT}')
    return len(loads)


def inspect_apk(path, build_tools):
    output = subprocess.run([str(build_tools / "zipalign"), "-c", "-v", "-P", "16", "4", str(path)],
                            check=True, capture_output=True, text=True).stdout
    (path.parent / (path.stem + "-zipalign.txt")).write_text(output)
    entries = []
    with zipfile.ZipFile(path) as archive:
        raw_apk = path.read_bytes()
        for info in archive.infolist():
            match = re.fullmatch(r"lib/([^/]+)/libpersona_core\.so", info.filename)
            if match:
                if info.compress_type != zipfile.ZIP_STORED:
                    raise ValueError(f"{path.name}: {info.filename} is compressed")
                header = raw_apk[info.header_offset:info.header_offset + 30]
                if len(header) != 30 or header[:4] != b'PK\x03\x04':
                    raise ValueError(f"{path.name}: {info.filename} has an invalid local header")
                name_length, extra_length = struct.unpack_from('<HH', header, 26)
                # Alignment is of the file data, not the local header's own offset. These lengths are from
                # the local header (central-directory extras can differ).
                data_offset = info.header_offset + 30 + name_length + extra_length
                if data_offset % ALIGNMENT:
                    raise ValueError(f"{path.name}: {info.filename} data offset {data_offset} is not 16 KB aligned")
                entries.append({"abi": match.group(1), "packageBytes": info.file_size,
                                "compressedBytes": info.compress_size, "stored": True,
                                "dataOffset": data_offset, "elfLoadSegments": inspect_elf(archive.read(info), match.group(1))})
    if {entry["abi"] for entry in entries} != set(ABIS):
        raise ValueError(f"{path.name}: expected both {', '.join(ABIS)}")
    return entries


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--native", type=Path, required=True, help="build-android.sh output directory")
    parser.add_argument("--symbols", type=Path, required=True, help="symbol archive directory, outside jniLibs")
    parser.add_argument("--apk", type=Path, required=True)
    parser.add_argument("--build-tools", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True, help="new or existing report directory")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    rows = []
    for abi in ABIS:
        raw = args.symbols / abi / "libpersona_core.so"
        stripped = args.native / abi / "libpersona_core.so"
        for path in (raw, stripped):
            if not path.is_file():
                raise ValueError("Missing native library: " + str(path))
        readelf = args.build_tools.parent.parent / 'ndk/29.0.14206865/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-strip'
        if not readelf.is_file():
            raise ValueError('Pinned NDK llvm-strip is required to verify the symbol archive: ' + str(readelf))
        comparison = args.output / (abi + '-strip-check.so')
        subprocess.run([str(readelf), '--strip-unneeded', '-o', str(comparison), str(raw)], check=True)
        if comparison.read_bytes() != stripped.read_bytes():
            raise ValueError(abi + ': raw and stripped libraries do not come from the same build')
        rows.append({"abi": abi, "rawBytes": raw.stat().st_size, "strippedBytes": stripped.stat().st_size,
                     "rawSha256": hashlib.sha256(raw.read_bytes()).hexdigest(),
                     "strippedSha256": hashlib.sha256(stripped.read_bytes()).hexdigest(),
                     "strippedGzipBytes": len(gzip.compress(stripped.read_bytes(), mtime=0))})
    with zipfile.ZipFile(args.apk) as package:
        archive_bytes = {entry["abi"]: package.read(f'lib/{entry["abi"]}/libpersona_core.so')
                         for entry in inspect_apk(args.apk, args.build_tools)}
    for entry in inspect_apk(args.apk, args.build_tools):
        row = next(row for row in rows if row["abi"] == entry["abi"])
        row.update(entry)
        if args.native.joinpath(entry["abi"], "libpersona_core.so").read_bytes() != archive_bytes[entry["abi"]]:
            raise ValueError(entry["abi"] + ": packaged and stripped libraries are not byte-identical")
        if row["packageBytes"] != row["strippedBytes"]:
            raise ValueError(entry["abi"] + ": packaged and stripped library differ")
    result = {"apk": args.apk.name, "elfLoadAlignmentBytes": ALIGNMENT,
              "zipStoredAnd16KBPadded": True, "abis": rows}
    (args.output / "abi-sizes.json").write_text(json.dumps(result, indent=2) + "\n")
    with (args.output / "abi-sizes.csv").open("w", newline="") as output:
        writer = csv.DictWriter(output, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)
    for row in rows:
        print("Android core size: {abi} raw={rawBytes} stripped={strippedBytes} gzip={strippedGzipBytes} "
              "package={packageBytes} bytes".format(**row))
    print(f"Android core APK: PASS ({args.apk.name}; both libraries STORED, zipalign -P 16 passed)")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, struct.error, subprocess.CalledProcessError, zipfile.BadZipFile) as error:
        sys.exit("Android core size: FAILED (" + str(error) + ")")
