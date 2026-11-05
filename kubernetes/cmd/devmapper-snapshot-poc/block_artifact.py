#!/usr/bin/env python3
# Copyright 2026 Alibaba Group Holding Ltd.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Pack and apply an experimental dm-thin changed-block artifact."""

import argparse
import gzip
import hashlib
import json
import os
import struct
import subprocess
import sys
import time
import xml.etree.ElementTree as ET

MAGIC = b"OSBDM01\n"
CHUNK = 4 * 1024 * 1024
DATA_TYPES = {"different", "right_only"}


def copy_exact(source, target, count, digest=None):
    remaining = count
    while remaining:
        data = source.read(min(CHUNK, remaining))
        if not data:
            raise EOFError(f"unexpected EOF with {remaining} bytes remaining")
        target.write(data)
        if digest is not None:
            digest.update(data)
        remaining -= len(data)


def pack(args):
    root = ET.parse(args.delta).getroot()
    block_size = int(root.attrib["data_block_size"]) * 512
    extents = []
    payload_bytes = 0
    for element in root.iter():
        if element.tag not in ("different", "right_only", "left_only"):
            continue
        extent = {
            "type": element.tag,
            "begin": int(element.attrib["begin"]),
            "length": int(element.attrib["length"]),
        }
        extents.append(extent)
        if element.tag in DATA_TYPES:
            payload_bytes += extent["length"] * block_size
    header = {
        "format": "opensandbox.devmapper.block.v1",
        "base": args.base,
        "blockSize": block_size,
        "virtualSectors": args.virtual_sectors,
        "extents": extents,
        "uncompressedPayloadBytes": payload_bytes,
    }
    header_data = json.dumps(header, separators=(",", ":")).encode()
    digest = hashlib.sha256()
    started = time.monotonic()
    with open(args.source, "rb", buffering=0) as source, gzip.open(args.output, "wb", compresslevel=args.level) as output:
        output.write(MAGIC)
        output.write(struct.pack(">Q", len(header_data)))
        output.write(header_data)
        for extent in extents:
            if extent["type"] not in DATA_TYPES:
                continue
            offset = extent["begin"] * block_size
            count = extent["length"] * block_size
            source.seek(offset)
            copy_exact(source, output, count, digest)
    elapsed = time.monotonic() - started
    result = {
        "artifact": args.output,
        "artifactBytes": os.path.getsize(args.output),
        "payloadBytes": payload_bytes,
        "payloadSHA256": digest.hexdigest(),
        "extentCount": len(extents),
        "durationSeconds": elapsed,
    }
    print(json.dumps(result, separators=(",", ":")))


def apply(args):
    digest = hashlib.sha256()
    started = time.monotonic()
    with gzip.open(args.artifact, "rb") as source, open(args.target, "r+b", buffering=0) as target:
        if source.read(len(MAGIC)) != MAGIC:
            raise ValueError("invalid block artifact magic")
        header_size = struct.unpack(">Q", source.read(8))[0]
        header = json.loads(source.read(header_size))
        if header.get("format") != "opensandbox.devmapper.block.v1":
            raise ValueError("unsupported block artifact format")
        block_size = int(header["blockSize"])
        for extent in header["extents"]:
            offset = int(extent["begin"]) * block_size
            count = int(extent["length"]) * block_size
            if extent["type"] in DATA_TYPES:
                target.seek(offset)
                copy_exact(source, target, count, digest)
            elif extent["type"] == "left_only":
                subprocess.run(
                    ["blkdiscard", "--offset", str(offset), "--length", str(count), args.target],
                    check=True,
                )
        target.flush()
        os.fsync(target.fileno())
        trailing = source.read(1)
        if trailing:
            raise ValueError("artifact has trailing payload data")
    elapsed = time.monotonic() - started
    print(json.dumps({
        "target": args.target,
        "payloadSHA256": digest.hexdigest(),
        "durationSeconds": elapsed,
        "base": header["base"],
    }, separators=(",", ":")))


def inspect(args):
    with gzip.open(args.artifact, "rb") as source:
        if source.read(len(MAGIC)) != MAGIC:
            raise ValueError("invalid block artifact magic")
        header_size = struct.unpack(">Q", source.read(8))[0]
        print(json.dumps(json.loads(source.read(header_size)), indent=2))


def main():
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    pack_parser = commands.add_parser("pack")
    pack_parser.add_argument("--delta", required=True)
    pack_parser.add_argument("--source", required=True)
    pack_parser.add_argument("--output", required=True)
    pack_parser.add_argument("--base", required=True)
    pack_parser.add_argument("--virtual-sectors", required=True, type=int)
    pack_parser.add_argument("--level", type=int, default=1)
    pack_parser.set_defaults(function=pack)
    apply_parser = commands.add_parser("apply")
    apply_parser.add_argument("--artifact", required=True)
    apply_parser.add_argument("--target", required=True)
    apply_parser.set_defaults(function=apply)
    inspect_parser = commands.add_parser("inspect")
    inspect_parser.add_argument("--artifact", required=True)
    inspect_parser.set_defaults(function=inspect)
    args = parser.parse_args()
    args.function(args)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"ERROR: {error}", file=sys.stderr)
        raise
