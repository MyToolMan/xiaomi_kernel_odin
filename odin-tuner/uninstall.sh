#!/system/bin/sh
MODDIR=${0%/*}
DATA_DIR=/data/adb/odin-tuner
SOCKET="$DATA_DIR/odin-tuner.sock"
PID_FILE="$DATA_DIR/daemon.pid"
BIN="$MODDIR/bin/odin-tuner"

if [ -s "$DATA_DIR/web.pid" ]; then
  web_pid=$(cat "$DATA_DIR/web.pid" 2>/dev/null)
  if [ -n "$web_pid" ] && kill -0 "$web_pid" 2>/dev/null; then
    kill -TERM "$web_pid" 2>/dev/null || true
  fi
  rm -f "$DATA_DIR/web.pid"
fi
rm -f "$DATA_DIR/web_token"

restore_ok=0
if [ -x "$BIN" ] && [ -S "$SOCKET" ]; then
  if "$BIN" restore-all --socket "$SOCKET"; then
    restore_ok=1
  fi
fi

if [ -s "$PID_FILE" ]; then
  pid=$(cat "$PID_FILE" 2>/dev/null)
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    i=0
    while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 20 ]; do
      i=$((i + 1))
      sleep 0.25
    done
    kill -0 "$pid" 2>/dev/null && kill -KILL "$pid" 2>/dev/null || true
  fi
  rm -f "$PID_FILE"
fi

rm -f "$SOCKET"

# A successful restore response removes each verified backup file.
if [ -d "$DATA_DIR/backup" ] && find "$DATA_DIR/backup" -type f | grep -q .; then
  echo "odin-tuner: backup files remain; restore was not confirmed" >&2
  exit 1
fi

if [ "$restore_ok" -eq 1 ]; then
  echo "odin-tuner: all changed nodes restored"
fi
exit 0
