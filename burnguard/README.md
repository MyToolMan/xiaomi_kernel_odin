# VOLT FORGE Burn Guard

Static, opaque top-strip mask for MIX4 `odin`.

## Cost model

- One static `TYPE_APPLICATION_OVERLAY` surface, created once.
- No animation, render loop, polling, timer, sensor, network, or wake lock.
- Relayout only when Android reports a configuration change such as rotation.
- The mask is below the status bar, so status icons remain interactive.

## Install and control

```sh
cd burnguard
./build.sh
adb install -r build/burnguard.apk
adb shell am start -n com.miuitoolman.voltforge.burnguard/.MainActivity
```

On first launch, approve the one-time “display over other apps” permission.
MIUI requires this approval through its permission UI; directly forcing the
AppOps package mode is not reliable on this ROM. The service then starts
automatically.

The VOLT FORGE module also launches this activity in silent autostart mode
after its root daemon is ready. MIUI's own BOOT_COMPLETED delivery is not
reliable for this package, so the root module path is authoritative.

Stop the mask without uninstalling:

```sh
adb shell am force-stop com.miuitoolman.voltforge.burnguard
```

The foreground notification makes the service's lifetime explicit and prevents
silent background kills. It is a low-importance ongoing notification.

The helper can also perform the common actions:

```sh
./ctl.sh install
./ctl.sh start
./ctl.sh status
./ctl.sh stop
./ctl.sh uninstall
```

## Measured behavior

On the test device Android clamps third-party application overlays to 80%
opacity. The 1080 x 104 strip therefore removes roughly 80% of emitted
luminance rather than reaching mathematical black. It remains a single static
surface with zero sampled scheduler runtime over a 10-second idle window.
