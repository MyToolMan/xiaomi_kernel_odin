#!/system/bin/sh
MODDIR=$(cd "${0%/*}/.." && pwd)

"$MODDIR/bin/stop-web.sh" >/dev/null 2>&1 || true
exec "$MODDIR/bin/start-web.sh"
