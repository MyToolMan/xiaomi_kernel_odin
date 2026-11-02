#!/system/bin/sh
MODDIR=${0%/*}
DATA_DIR=/data/adb/odin-tuner
LOG_DIR="$DATA_DIR/logs"
LOG_FILE="$LOG_DIR/daemon.log"
PID_FILE="$DATA_DIR/daemon.pid"
SOCKET="$DATA_DIR/odin-tuner.sock"
BIN="$MODDIR/bin/odin-tuner"

start_web_if_configured() {
  web_autostart=$("$BIN" config-get --config "$DATA_DIR/config.json" --field web_autostart 2>/dev/null || echo false)
  if [ "$web_autostart" = "true" ]; then
    "$MODDIR/bin/start-web.sh" >> "$LOG_FILE" 2>&1 || true
  fi
}

mkdir -p "$DATA_DIR/backup" "$LOG_DIR"
chmod 700 "$DATA_DIR" "$DATA_DIR/backup" "$LOG_DIR"
if [ ! -f "$DATA_DIR/config.json" ]; then
  cp "$MODDIR/config.json" "$DATA_DIR/config.json"
  chmod 600 "$DATA_DIR/config.json"
fi

if [ -s "$PID_FILE" ]; then
  old_pid=$(cat "$PID_FILE" 2>/dev/null)
  if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null &&
     grep -q odin-tuner "/proc/$old_pid/cmdline" 2>/dev/null; then
    echo "odin-tuner already running: pid=$old_pid"
    start_web_if_configured
    exit 0
  fi
fi

ready=0
i=0
while [ "$i" -lt 60 ]; do
  if [ -r /sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq ] &&
     [ -r /sys/devices/platform/soc/3d00000.qcom,kgsl-3d0/devfreq/3d00000.qcom,kgsl-3d0/max_freq ] &&
     [ -d /sys/class/thermal ]; then
    ready=1
    break
  fi
  i=$((i + 1))
  sleep 2
done

if [ "$ready" -ne 1 ]; then
  echo "timed out waiting for CPU/GPU/thermal nodes" >> "$LOG_FILE"
  exit 1
fi

{
  echo "starting $(date -Iseconds 2>/dev/null || date)"
  echo "module=$MODDIR"
} >> "$LOG_FILE"

nohup "$BIN" daemon \
  --data-dir "$DATA_DIR" \
  --module-dir "$MODDIR" \
  --socket "$SOCKET" >> "$LOG_FILE" 2>&1 &

pid=$!
echo "$pid" > "$PID_FILE"
chmod 600 "$PID_FILE"

i=0
while [ "$i" -lt 20 ]; do
  if [ -S "$SOCKET" ]; then
    chown 0:2000 "$SOCKET" 2>/dev/null || true
    chmod 660 "$SOCKET"
    echo "odin-tuner ready: pid=$pid socket=$SOCKET"
    start_web_if_configured
    exit 0
  fi
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "odin-tuner exited during startup" >> "$LOG_FILE"
    rm -f "$PID_FILE"
    exit 1
  fi
  i=$((i + 1))
  sleep 1
done

echo "odin-tuner socket timeout" >> "$LOG_FILE"
kill "$pid" 2>/dev/null || true
rm -f "$PID_FILE"
exit 1
