#!/system/bin/sh
MODDIR=$(cd "${0%/*}/.." && pwd)
DATA_DIR="${ODIN_DATA_DIR:-/data/adb/odin-tuner}"
BIN="$MODDIR/bin/odin-tuner"
LOG_DIR="$DATA_DIR/logs"
LOG_FILE="$LOG_DIR/web.log"
PID_FILE="$DATA_DIR/web.pid"
TOKEN_FILE="$DATA_DIR/web_token"
LISTEN="${ODIN_WEB_LISTEN:-0.0.0.0:8787}"

umask 077
mkdir -p "$DATA_DIR" "$LOG_DIR"

if [ -s "$PID_FILE" ]; then
  old_pid=$(cat "$PID_FILE" 2>/dev/null)
  if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
    kill -TERM "$old_pid" 2>/dev/null || true
    i=0
    while kill -0 "$old_pid" 2>/dev/null && [ "$i" -lt 20 ]; do
      i=$((i + 1))
      sleep 0.25
    done
  fi
  rm -f "$PID_FILE"
fi

token=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \r\n')
if [ -z "$token" ]; then
  echo "failed to generate web token" >&2
  exit 1
fi
printf '%s\n' "$token" > "$TOKEN_FILE"
chmod 600 "$TOKEN_FILE"

nohup "$BIN" web \
  --data-dir "$DATA_DIR" \
  --socket "$DATA_DIR/odin-tuner.sock" \
  --listen "$LISTEN" \
  --token-file "$TOKEN_FILE" \
  --root "$MODDIR/webui" >> "$LOG_FILE" 2>&1 &
pid=$!
echo "$pid" > "$PID_FILE"
chmod 600 "$PID_FILE"
sleep 1

if kill -0 "$pid" 2>/dev/null; then
  phone_ip=$(ip route get 1.1.1.1 2>/dev/null | awk '
    { for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit } }
  ')
  [ -n "$phone_ip" ] || phone_ip="<手机IP>"
  listen_port=${LISTEN##*:}
  echo "web token: $token"
  echo "local: http://127.0.0.1:$listen_port/?token=$token"
  echo "LAN:   http://$phone_ip:$listen_port/?token=$token"
  echo "listen: $LISTEN"
else
  rm -f "$PID_FILE"
  echo "web UI failed; see $LOG_FILE" >&2
  exit 1
fi
