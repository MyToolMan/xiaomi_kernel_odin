#!/usr/bin/env python3
"""Replace the kernel in an Android boot image while keeping its ramdisk."""

from __future__ import annotations

import argparse
import hashlib
import struct
from pathlib import Path

FIXED_PAGE_SIZE = 4096


def align(value: int, page_size: int) -> int:
    return (value + page_size - 1) // page_size * page_size


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("image", type=Path)
    parser.add_argument("base_boot", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()

    base = args.base_boot.read_bytes()
    kernel = args.image.read_bytes()
    if base[:8] != b"ANDROID!":
        raise SystemExit("base image is not an Android boot image")

    original_kernel_size, ramdisk_size = struct.unpack_from("<II", base, 8)
    header_size = struct.unpack_from("<I", base, 0x14)[0]
    header_version = struct.unpack_from("<I", base, 40)[0]
    if header_version not in (2, 3):
        raise SystemExit(f"unsupported boot header version: {header_version}")

    page_size = FIXED_PAGE_SIZE if header_version >= 3 else struct.unpack_from("<I", base, 36)[0]
    if page_size <= 0 or page_size & (page_size - 1):
        raise SystemExit(f"invalid page size: {page_size}")

    ramdisk_start = align(page_size + original_kernel_size, page_size)
    ramdisk = base[ramdisk_start : ramdisk_start + ramdisk_size]
    if len(ramdisk) != ramdisk_size:
        raise SystemExit("base image is truncated")

    header = bytearray(base[:header_size])
    struct.pack_into("<I", header, 8, len(kernel))
    struct.pack_into("<I", header, 12, len(ramdisk))

    packed = bytearray()
    packed.extend(header)
    packed.extend(bytes(align(len(header), page_size) - len(header)))
    packed.extend(kernel)
    packed.extend(bytes(align(len(kernel), page_size) - len(kernel)))
    packed.extend(ramdisk)
    packed.extend(bytes(align(len(ramdisk), page_size) - len(ramdisk)))

    # Keep the output usable with dd while omitting the now-invalid AVB footer.
    if len(packed) <= len(base):
        packed.extend(bytes(len(base) - len(packed)))
    else:
        raise SystemExit("new boot image is larger than the original partition image")

    args.output.write_bytes(packed)
    print(f"header_version={header_version}")
    print(f"page_size={page_size}")
    print(f"kernel_size={len(kernel)}")
    print(f"ramdisk_size={len(ramdisk)}")
    print(f"kernel_sha256={sha256(kernel)}")
    print(f"ramdisk_sha256={sha256(ramdisk)}")
    print(f"output={args.output}")
    print(f"output_sha256={sha256(bytes(packed))}")


if __name__ == "__main__":
    main()
