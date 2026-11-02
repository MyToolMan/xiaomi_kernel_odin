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

Install `odin-tuner-v0.2.1.zip` from the SukiSU/Magisk module manager and reboot.
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

When `web_autostart` is `true`, `service.sh` starts the Web service after the
root daemon socket is ready. Open the module WebUI and use the restart/stop
buttons, or run:

```sh
su -c 'sh /data/adb/modules/odin-tuner/bin/start-web.sh'
su -c 'sh /data/adb/modules/odin-tuner/bin/restart-web.sh'
su -c 'sh /data/adb/modules/odin-tuner/bin/stop-web.sh'
```

`start-web.sh` is idempotent: it prints the active URL instead of starting a
second server. The default listen address is `0.0.0.0:8787`; override it with
`ODIN_WEB_LISTEN`.

Authentication is controlled by `auth_mode`:

- `off` (debug default) accepts requests without a token and logs a warning.
- `fixed` uses `fixed_token`; it must contain at least 16 URL-safe characters
  and survives restarts.
- `random` creates a new token in `/data/adb/odin-tuner/web_token` on every
  start.

In `fixed` and `random` modes, the page accepts `?token=...`, the
`X-ODIN-Token` header, or a Bearer token. If the browser URL contains `?token=`,
the frontend forwards it to the SSE stream automatically.

**Before publishing or exposing this module, change `auth_mode` from `off` to
`fixed` or `random`.** Use only on a trusted network and stop the service when
recording or testing is finished. Never expose port 8787 to the public internet.

The live page keeps 120 points, limits rendering to a maximum of 30 fps with
`requestAnimationFrame`, reports green/yellow/red SSE connection state, and
reconnects with exponential backoff from one second to 30 seconds. The daemon
sends an SSE heartbeat every 15 seconds.

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
  "hard_limit_temp": 95,
  "auth_mode": "off",
  "fixed_token": "",
  "web_autostart": true
}
```

The hard limit cannot be disabled and is clamped to at most 95 °C. CPU/GPU
thresholds are clamped to the configured hard limit. Configuration is reloaded
without restarting the daemon. Each trigger writes a structured `events.jsonl`
record containing sensor, temperature, parameter, and frequency before/after.

`auth_mode` defaults to `off` when absent so existing debug configs continue to
work. `web_autostart` defaults to `true` when absent. The module action button
restarts the Web service; use `stop-web.sh` to disable it until the next daemon
or device restart.

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
