#!/system/bin/sh
DATA_DIR="${ODIN_DATA_DIR:-/data/adb/odin-tuner}"
PID_FILE="$DATA_DIR/web.pid"

if [ -s "$PID_FILE" ]; then
  pid=$(cat "$PID_FILE" 2>/dev/null)
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
  fi
fi

rm -f "$PID_FILE" "$DATA_DIR/web_token"
echo "ODIN Tuner web service stopped"
