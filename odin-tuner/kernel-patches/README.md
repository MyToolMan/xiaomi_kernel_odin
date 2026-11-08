# GPU pwrlevel experiments

These patches target the MIX4 `lahaina` vendor-boot DTB. The active DTB on the
current device is `ro.boot.dtb_idx=0`.

## 855 MHz first stage

`gpu-pwrlevels-855.patch` changes the top power level for speed-bin 0, 5, and 7
from 840 MHz (`0x32116200`) to 855 MHz (`0x32f643c0`).

The following values deliberately remain stock:

- `qcom,level = <0x1a0>` (`TURBO_L1`, 416)
- `qcom,acd-level = <0x882a5ffd>`
- all DDR bus levels
- `qcom,initial-pwrlevel`

## Required workflow

1. Back up `boot_a`, `vendor_boot_a`, and `dtbo_a` and verify SHA-256.
2. Extract `vendor_boot_a` with `magiskboot unpack`.
3. Split the concatenated `dtb`, decompile the parts with `dtc`, and identify
   the DTB matching `ro.boot.dtb_idx`.
4. Repack and boot an unchanged DTS-to-DTB round trip first.
5. Apply one frequency or voltage-level change only.
6. Compile every DTB, concatenate them in the original order, and repack with
   both `dtb` and `ramdisk.cpio` present.
7. Verify `RAMDISK_SZ` and `DTB_SZ` before flashing.
8. Flash only `vendor_boot`, boot, and verify KGSL reports the target frequency.
9. Run idle, sustained graphics, mixed CPU/GPU, camera, suspend/resume, and
   repeated cold-boot tests.

Never combine frequency overclocking, regulator-level changes, ACD changes,
and a userspace GPU driver replacement in the same first experiment.

## Current result

The 855 MHz image boots repeatedly on the target device. KGSL reports:

```text
855000000 778000000 738000000 676000000 608000000
540000000 491000000 443000000 379000000 315000000
```

The `thermal-devfreq-0` cooling state is maintained at `0` by the
`performance` profile. The ODIN watchdog restores the maximum cooling state if
the GPU or hard temperature threshold is reached.

This is not yet approved as a daily configuration until sustained graphics
load and real-world regressions pass.
