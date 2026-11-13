# ODIN benchmark evidence

## 2026-11-12: 855 MHz stock-voltage baseline

Device: Xiaomi MIX4 (`odin`)

Test: 3DMark Wild Life Extreme Stress Test, 20-minute run

- Best loop score: `1185`
- Worst loop score: `1174`
- Stability: `99.1%`
- GPU load: `99%`
- Peak GPU temperature observed: approximately `69.8 C`
- Peak battery temperature observed: `37 C`
- KGSL snapshot fault count after run: `0`
- Start state: battery `100%`, charger connected, display kept awake
- GPU configuration: `855 MHz` ceiling, stock speed-bin 5 voltage corner `0x1a0`
- Result screenshot:
  `benchmarks/3dmark-wle-855-stock-voltage-baseline.png`

The first launch was invalid because Android's one-minute display timeout
paused the test. The reported baseline is from the complete second run with
stay-awake enabled.

## 2026-11-12: 855 MHz, experimental `0x180` voltage

Same 3DMark Wild Life Extreme Stress Test and stay-awake setup.

- Best loop score: `1535`
- Worst loop score: `1035`
- Stability: `67.4%`
- Peak GPU temperature observed: approximately `78 C`
- Peak battery temperature observed: `38 C`
- KGSL snapshot fault count after run: `0`
- Image:
  `out-odin/backups-20261109/vendor_boot_gpu855_v180.img`
- Image SHA256:
  `e2376115b9f89441a359484cfa64d554d79c0a0029d838ffc79ce5a5096d8964`
- Result screenshot:
  `benchmarks/3dmark-wle-855-v180-result.png`
- Raw telemetry:
  `benchmarks/3dmark-wle-855-v180-telemetry.txt`

This voltage point raises short-run peak performance, but the 20-loop test
falls from approximately 1500 to approximately 1035 after loop 11. It is not
accepted as the final sustained profile. The next test caps the GPU at
`738 MHz` with the same voltage point to test whether the useful part of the
curve can be retained without the late-run collapse.

## 2026-11-12: 855 MHz OPP, `0x180`, 738 MHz ceiling

The same image was tested with the daemon's GPU ceiling set to `738 MHz`.

- Best loop score: `1042`
- Worst loop score: `1035`
- Stability: `99.3%`
- Peak GPU temperature observed: approximately `60.5 C`
- Peak battery temperature observed: `36 C`
- KGSL snapshot fault count after run: `0`
- Result screenshot:
  `benchmarks/3dmark-wle-855-v180-cap738-result.png`
- Raw telemetry:
  `benchmarks/3dmark-wle-855-v180-cap738-telemetry.txt`

The stable result is lower than the stock-voltage baseline (approximately
12% below `1185`). The GPU governor mostly selected `540 MHz` even with the
738 MHz ceiling, so this compromise is rejected.

## Invalid governor experiment

Forcing `performance` while setting a 540 MHz minimum produced an inconsistent
driver state: the governor reported performance and the minimum stayed at
540 MHz, while the effective frequency remained at 315 MHz. A one-minute
Wild Life Extreme run scored only `566` at `3.39 FPS`. That result is invalid
and is not used.

## Wild Life Extreme single-run checkpoint

After restoring the normal `msm-adreno-tz` governor and the normal 315 MHz
minimum, the first valid single-run checkpoint was:

- Overall score: `1027`
- Average frame rate: `6.15 FPS`
- Configuration: 855 MHz OPP, `0x180` voltage, 90 C soft limit, 95 C hard limit
- Result screenshot:
  `benchmarks/3dmark-wle-v180-showcase-result.png`

This is a checkpoint, not yet a final A/B result because a stock-voltage run
under the same single-run method is still required.

## Stock-original Wild Life Extreme baseline

The original `vendor_boot_a` image was restored and the daemon profile was set
to `system`/manual. A clean one-minute Wild Life Extreme run produced:

- Overall score: `1204`
- Average frame rate: `7.21 FPS`
- Effective GPU clock during load: `608 MHz`
- KGSL snapshot fault count: `0`
- Stock partition SHA256:
  `344568dd0c262138abb7926d20c6d1c28ade7323f36ddd2fc56b236e57243e07`
- Result screenshot:
  `benchmarks/3dmark-wle-stock-original-system-result.png`
- Raw telemetry:
  `benchmarks/3dmark-wle-stock-original-system-telemetry.txt`

This is the primary whole-device baseline for later comparisons. The earlier
`1027` checkpoint used a different daemon/profile state and must not be
presented as a direct voltage comparison.

## Stock voltage vs `0x180`: matched single-run A/B

Both runs used the original CPU profile state, `msm-adreno-tz`, a 315 MHz GPU
minimum, and the same 1-minute Wild Life Extreme workload.

| Configuration | Score | Average FPS | Peak GPU temp |
| --- | ---: | ---: | ---: |
| Stock partition, 840 MHz ceiling | 1204 | 7.21 | about 54.2 C |
| 855 MHz OPP at `0x180` | 1211 | 7.26 | about 56.2 C |

The difference is only about 0.6%, while the experimental sample ran hotter.
It is within normal run-to-run variation and is not a meaningful performance
gain. Therefore `0x180` is rejected. The 855 MHz OPP is retained at its stock
`0x1a0` voltage because that configuration previously passed the full
20-minute stress test at 99.1% stability.

- Experimental result screenshot:
  `benchmarks/3dmark-wle-v180-system-result.png`
- Experimental telemetry:
  `benchmarks/3dmark-wle-v180-system-telemetry.txt`

## Final selected state

- `vendor_boot_a`: 855 MHz top OPP at stock `0x1a0` voltage
- Image SHA256:
  `bdc4191f86440548efadde27e14ba338d105d95f975331a695f5f961c1d4de5d`
- CPU ceilings: `1804800 / 2419200 / 2995200 kHz`
- GPU ceiling: `855000000 Hz`
- GPU governor: `msm-adreno-tz`
- GPU thermal cooling state: `0`
- Soft throttle threshold: `85 C`
- Hard limit: `95 C`
- Startup profile: `performance` with enforcement enabled

This state previously completed the 20-loop stress test at 99.1% stability
with zero KGSL faults. The experimental `0x180` voltage image was removed.

## 900 MHz candidate failure

A higher top OPP was prepared by using the 900 MHz / `0x1b0` configuration
already present for other GPU speed bins in the same vendor DTB.

- Candidate image SHA256:
  `fb35fb0a49ea279111cac9ac06233b2734b023083d4edbd8e3531e352fad4a72`
- Result: boot stalled on the first screen
- Recovery: manually entered fastboot and reflashed the stable 855 MHz image
- Final verified partition SHA256:
  `bdc4191f86440548efadde27e14ba338d105d95f975331a695f5f961c1d4de5d`

The 900 MHz point is rejected and must not be flashed.

## 855 MHz OPP with a 738 MHz minimum

Keeping the stable stock-voltage image and normal `msm-adreno-tz` governor,
the GPU minimum was raised from 315 MHz to 738 MHz:

- Overall score: `1484`
- Average frame rate: `8.89 FPS`
- Stock-original score gain: approximately 23.3%
- Effective GPU clock during load: `738-778 MHz`
- Peak GPU temperature observed: approximately `76.7 C`
- KGSL snapshot fault count: `0`
- Temporary soft limit: `90 C`; hard limit remained `95 C`
- Result screenshot:
  `benchmarks/3dmark-wle-v855-min738-result.png`
- Raw telemetry:
  `benchmarks/3dmark-wle-v855-min738-telemetry.txt`

This is the first configuration with a material performance gain. It still
requires the full 20-loop stress test before it can be selected as the final
performance profile.

### 20-loop validation

- Best loop score: `1492`
- Worst loop score: `1233`
- Stability: `82.6%`
- Best-loop gain over the stock-original stress baseline: approximately 25.9%
- Worst-loop gain over the stock-original stress baseline: approximately 5.0%
- Result screenshot:
  `benchmarks/3dmark-wle-v855-min738-stress-result.png`
- Raw telemetry:
  `benchmarks/3dmark-wle-v855-min738-stress-telemetry.txt`

The profile passed the full 20-loop run without touching the 90 C soft limit;
the observed peak remained below 79 C. It is retained as the `EXTREME`
profile with the hard shutdown at 95 C.
