#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ADB=${ADB:-adb}
PACKAGE=com.miuitoolman.voltforge.burnguard
ACTIVITY="$PACKAGE/.MainActivity"

command_name=${1:-status}
shift || true

if [ -n "${SERIAL:-}" ]; then
  set -- "$ADB" -s "$SERIAL" "$@"
else
  set -- "$ADB" "$@"
fi

case "$command_name" in
  build)
    "$ROOT_DIR/build.sh"
    ;;
  install)
    "$ROOT_DIR/build.sh"
    "$@" install -r "$ROOT_DIR/build/burnguard.apk"
    "$@" shell am start -n "$ACTIVITY"
    ;;
  start)
    "$@" shell am start -n "$ACTIVITY"
    ;;
  stop)
    "$@" shell am force-stop "$PACKAGE"
    ;;
  status)
    "$@" shell "dumpsys activity services $PACKAGE | grep -E 'ServiceRecord|isForeground|startRequested' || true"
    "$@" shell "dumpsys window windows | grep -A4 'VOLT FORGE Burn Guard' | grep -E 'mAttrs=|isVisible=|Frames:' || true"
    "$@" shell "pid=\$(pidof $PACKAGE || true); if [ -n \"\$pid\" ]; then grep -E 'VmRSS|Threads' /proc/\$pid/status; fi"
    ;;
  uninstall)
    "$@" shell am force-stop "$PACKAGE" || true
    "$@" uninstall "$PACKAGE"
    ;;
  *)
    echo "usage: $0 [build|install|start|stop|status|uninstall]" >&2
    exit 2
    ;;
esac
