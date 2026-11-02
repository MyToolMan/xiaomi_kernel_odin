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

Install `odin-tuner-v0.1.0.zip` from the SukiSU/Magisk module manager and reboot.
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

The HTTP server is not started by `service.sh`. Run the module action or:

```sh
su -c 'sh /data/adb/modules/odin-tuner/bin/start-web.sh'
```

Then open:

```text
http://127.0.0.1:8787
```

## API

The control API is available over the Unix socket only:

- `GET /snapshot`
- `POST /set` with `{"param":"cpu_little_max_freq","value":1209600}`
- `POST /restore`

Current tunable parameter names:

- `cpu_little_max_freq`
- `cpu_big_max_freq`
- `cpu_prime_max_freq`
- `gpu_max_freq`, only when the root daemon can access the KGSL devfreq node

Every write is range-checked, written, read back, and rolled back on failure.
The first write stores the original value under `backup/`. `SIGTERM` and
module uninstall both invoke the restore path.

## Watchdog

At 80 °C the daemon caps every managed CPU/GPU maximum to a conservative
frequency and writes a structured event to `events.jsonl`. It rearms after the
maximum valid thermal reading drops below 75 °C.

## Deferred to v2

- KGSL nodes hidden from adb shell by SELinux
- `zram0` compression algorithm
- `vm.swappiness`
- Tuning controls in the Web UI
