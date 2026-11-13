#!/usr/bin/env python3
"""Patch one GPU OPP voltage corner in a flattened device tree blob."""

from __future__ import annotations

import argparse
import pathlib
import struct
import sys

FDT_MAGIC = 0xD00DFEED
FDT_BEGIN_NODE = 1
FDT_END_NODE = 2
FDT_PROP = 3
FDT_NOP = 4
FDT_END = 9
HEADER = struct.Struct(">10I")


def align4(value: int) -> int:
    return (value + 3) & ~3


def read_cstring(blob: bytes, offset: int) -> tuple[str, int]:
    end = blob.index(0, offset)
    return blob[offset:end].decode("ascii"), end + 1


class Fdt:
    def __init__(self, blob: bytes):
        if len(blob) < HEADER.size:
            raise ValueError("blob is shorter than the FDT header")
        fields = HEADER.unpack_from(blob)
        if fields[0] != FDT_MAGIC:
            raise ValueError("not a flattened device tree")
        self.blob = bytearray(blob)
        self.total_size = fields[1]
        self.struct_offset = fields[2]
        self.strings_offset = fields[3]
        self.struct_size = fields[9]
        if self.total_size > len(self.blob):
            raise ValueError("FDT total size exceeds file size")

    def property_offset(
        self, speed_bin: int, pwrlevel: int, property_name: str
    ) -> int:
        target_level = f"qcom,gpu-pwrlevel@{pwrlevel}"
        position = self.struct_offset
        end = self.struct_offset + self.struct_size
        stack: list[str] = []
        matches: list[int] = []
        target_prefix: tuple[str, ...] | None = None

        while position < end:
            token = struct.unpack_from(">I", self.blob, position)[0]
            position += 4
            if token == FDT_BEGIN_NODE:
                name, position = read_cstring(self.blob, position)
                position = self.struct_offset + align4(position - self.struct_offset)
                stack.append(name or "/")
            elif token == FDT_END_NODE:
                if not stack:
                    raise ValueError("unbalanced FDT node stream")
                stack.pop()
            elif token == FDT_PROP:
                length, name_offset = struct.unpack_from(">II", self.blob, position)
                position += 8
                data_offset = position
                position += align4(length)
                if not stack:
                    continue
                property_name_at_node, _ = read_cstring(
                    self.blob, self.strings_offset + name_offset
                )
                if (
                    property_name_at_node == "qcom,speed-bin"
                    and stack[-1].startswith("qcom,gpu-pwrlevels-")
                    and length == 4
                    and struct.unpack_from(">I", self.blob, data_offset)[0]
                    == speed_bin
                ):
                    target_prefix = tuple(stack)
                if (
                    target_prefix is not None
                    and tuple(stack) == target_prefix + (target_level,)
                    and property_name_at_node == property_name
                ):
                    if length != 4:
                        raise ValueError(
                            f"{property_name} has length {length}, expected 4"
                        )
                    matches.append(data_offset)
            elif token in (FDT_NOP,):
                continue
            elif token == FDT_END:
                break
            else:
                raise ValueError(f"unsupported FDT token 0x{token:x}")

        if len(matches) != 1:
            raise ValueError(
                f"expected one match for speed-bin {speed_bin}/{target_level}, "
                f"found {len(matches)}"
            )
        return matches[0]

    def set_u32(self, offset: int, value: int) -> tuple[int, int]:
        before = struct.unpack_from(">I", self.blob, offset)[0]
        struct.pack_into(">I", self.blob, offset, value)
        return before, value


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("input", type=pathlib.Path)
    parser.add_argument("output", type=pathlib.Path)
    parser.add_argument("--speed-bin", type=lambda value: int(value, 0), default=5)
    parser.add_argument("--pwrlevel", type=int, default=0)
    parser.add_argument("--property", default="qcom,level")
    parser.add_argument(
        "--value", type=lambda value: int(value, 0), required=True
    )
    args = parser.parse_args()

    fdt = Fdt(args.input.read_bytes())
    offset = fdt.property_offset(args.speed_bin, args.pwrlevel, args.property)
    before, after = fdt.set_u32(offset, args.value)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_bytes(fdt.blob)
    print(
        f"speed-bin={args.speed_bin} pwrlevel={args.pwrlevel} "
        f"{args.property}: 0x{before:x} -> 0x{after:x}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
