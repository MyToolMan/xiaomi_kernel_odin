#!/system/bin/sh
MODDIR=$(cd "${0%/*}/.." && pwd)
DATA_DIR="${ODIN_DATA_DIR:-/data/adb/odin-tuner}"
BIN="$MODDIR/bin/odin-tuner"
LOG_DIR="$DATA_DIR/logs"
LOG_FILE="$LOG_DIR/web.log"
PID_FILE="$DATA_DIR/web.pid"
TOKEN_FILE="$DATA_DIR/web_token"
CONFIG_FILE="$DATA_DIR/config.json"
LISTEN="${ODIN_WEB_LISTEN:-0.0.0.0:8787}"

umask 077
mkdir -p "$DATA_DIR" "$LOG_DIR"

auth_mode=$("$BIN" config-get --config "$CONFIG_FILE" --field auth_mode) || exit 1
listen_port=${LISTEN##*:}
phone_ip=$(ip route get 1.1.1.1 2>/dev/null | awk '
  { for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit } }
')
[ -n "$phone_ip" ] || phone_ip="<手机IP>"

print_urls() {
  token="$1"
  mode="$2"
  if [ "$mode" = "off" ]; then
    echo "local: http://127.0.0.1:$listen_port/"
    echo "LAN:   http://$phone_ip:$listen_port/"
  else
    echo "web token: $token"
    echo "local: http://127.0.0.1:$listen_port/?token=$token"
    echo "LAN:   http://$phone_ip:$listen_port/?token=$token"
  fi
  echo "listen: $LISTEN"
}

if [ -s "$PID_FILE" ]; then
  old_pid=$(cat "$PID_FILE" 2>/dev/null)
  if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null &&
     tr '\000' ' ' < "/proc/$old_pid/cmdline" 2>/dev/null | grep -q "odin-tuner web"; then
    token=$(cat "$TOKEN_FILE" 2>/dev/null)
    echo "web service already running: pid=$old_pid"
    print_urls "$token" "$auth_mode"
    exit 0
  fi
  rm -f "$PID_FILE"
fi

case "$auth_mode" in
  off)
    token=""
    rm -f "$TOKEN_FILE"
    echo "WARNING: 鉴权已关闭，仅限可信网络"
    ;;
  fixed)
    token=$("$BIN" config-get --config "$CONFIG_FILE" --field fixed_token) || exit 1
    if [ -z "$token" ]; then
      echo "fixed_token is empty" >&2
      exit 1
    fi
    printf '%s\n' "$token" > "$TOKEN_FILE"
    chmod 600 "$TOKEN_FILE"
    ;;
  random)
    token=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \r\n')
    if [ -z "$token" ]; then
      echo "failed to generate web token" >&2
      exit 1
    fi
    printf '%s\n' "$token" > "$TOKEN_FILE"
    chmod 600 "$TOKEN_FILE"
    ;;
  *)
    echo "invalid auth_mode: $auth_mode" >&2
    exit 1
    ;;
esac

nohup "$BIN" web \
  --data-dir "$DATA_DIR" \
  --socket "$DATA_DIR/odin-tuner.sock" \
  --listen "$LISTEN" \
  --auth-mode "$auth_mode" \
  --token-file "$TOKEN_FILE" \
  --root "$MODDIR/webui" >> "$LOG_FILE" 2>&1 &
pid=$!
echo "$pid" > "$PID_FILE"
chmod 600 "$PID_FILE"
sleep 1

if kill -0 "$pid" 2>/dev/null &&
   tr '\000' ' ' < "/proc/$pid/cmdline" 2>/dev/null | grep -q "odin-tuner web"; then
  print_urls "$token" "$auth_mode"
else
  rm -f "$PID_FILE"
  echo "web UI failed; see $LOG_FILE" >&2
  exit 1
fi
