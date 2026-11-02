# ODIN Tuner Skeleton

First-pass, userspace-only tuning module for MIX4 `odin`.

## Scope

- No kernel driver or `.ko` is added.
- No debugfs, PMIC, DDR, DTS/DTBO, or boot-header access.
- A late-start daemon samples CPU frequency, GPU frequency, and thermal zones
  every two seconds.
- Writable parameters are exposed only when the daemon can discover and read
  the target node.
- The Web UI only reads `/snapshot`; it has no tuning controls.

## Install

Install `odin-tuner-v0.2.0.zip` from the SukiSU/Magisk module manager and reboot.
The daemon socket is:

```text
/data/adb/odin-tuner/odin-tuner.sock
```

Logs and backups:

```text
/data/adb/odin-tuner/logs/
/data/adb/odin-tuner/backup/
```

## Web UI

The HTTP server is not started by `service.sh`. Open the module WebUI and use
the start/stop buttons, or run:

```sh
su -c 'sh /data/adb/modules/odin-tuner/bin/start-web.sh'
su -c 'sh /data/adb/modules/odin-tuner/bin/stop-web.sh'
```

Each start creates a new random token in:

```text
/data/adb/odin-tuner/web_token
```

The script prints the token and LAN URL. The page requires either
`?token=...`, the `X-ODIN-Token` header, or a Bearer token. The default listen
address is `0.0.0.0:8787`; override it with `ODIN_WEB_LISTEN`.

Use only on a trusted network and stop the service when recording or testing is
finished. Do not expose port 8787 to the public internet.

SukiSU Manager also exposes the on-demand `webroot/` page. It reads the
daemon's `snapshot.json` through the KernelSU WebUI bridge.

## API

The control API is available over the Unix socket only:

- `GET /snapshot`
- `POST /set` with `{"param":"cpu_little_max_freq","value":1209600}`
- `POST /restore`
- `GET /stream` (SSE)

Current tunable parameter names:

- `cpu_little_max_freq`
- `cpu_big_max_freq`
- `cpu_prime_max_freq`
- `gpu_max_freq`, only when the root daemon can access the KGSL devfreq node

Every write is range-checked, written, read back, and rolled back on failure.
The first write stores the original value under `backup/`. `SIGTERM` and
module uninstall both invoke the restore path.

## Watchdog configuration

`/data/adb/odin-tuner/config.json`:

```json
{
  "cpu_throttle_temp": 85,
  "gpu_throttle_temp": 85,
  "hard_limit_temp": 95
}
```

The hard limit cannot be disabled and is clamped to at most 95 °C. CPU/GPU
thresholds are clamped to the configured hard limit. Configuration is reloaded
without restarting the daemon. Each trigger writes a structured `events.jsonl`
record containing sensor, temperature, parameter, and frequency before/after.

## Temperature summary

The frontend receives only these aggregates:

- CPU little: `cpu-0-*-step` and `cpu-0-*-usr`, maximum
- CPU mid: `cpu-1-0..3-step/usr`, maximum
- CPU prime: `cpu-1-4..7-step/usr`, maximum
- GPU: `gpuss-0-step` and `gpuss-1-step`, maximum
- battery: `battery`
- board: maximum of `xo_therm` and `quiet_therm`
- memory: `ddr-usr`

Zones with temperature <= -40 °C or mode `disabled` are excluded.

## Deferred to v2

- KGSL nodes hidden from adb shell by SELinux
- `zram0` compression algorithm
- `vm.swappiness`
- Tuning controls in the Web UI
