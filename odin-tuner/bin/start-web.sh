#!/system/bin/sh
MODDIR=$(cd "${0%/*}/.." && pwd)
DATA_DIR=/data/adb/odin-tuner
BIN="$MODDIR/bin/odin-tuner"
LOG_DIR="$DATA_DIR/logs"
LOG_FILE="$LOG_DIR/web.log"
PID_FILE="$DATA_DIR/web.pid"

mkdir -p "$LOG_DIR"

if [ -s "$PID_FILE" ]; then
  old_pid=$(cat "$PID_FILE" 2>/dev/null)
  if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
    echo "web UI already running: http://127.0.0.1:8787"
    exit 0
  fi
fi

nohup "$BIN" web \
  --data-dir "$DATA_DIR" \
  --socket "$DATA_DIR/odin-tuner.sock" \
  --listen 127.0.0.1:8787 \
  --root "$MODDIR/webui" >> "$LOG_FILE" 2>&1 &
pid=$!
echo "$pid" > "$PID_FILE"
chmod 600 "$PID_FILE"
sleep 1

if kill -0 "$pid" 2>/dev/null; then
  echo "web UI: http://127.0.0.1:8787"
else
  rm -f "$PID_FILE"
  echo "web UI failed; see $LOG_FILE" >&2
  exit 1
fi
