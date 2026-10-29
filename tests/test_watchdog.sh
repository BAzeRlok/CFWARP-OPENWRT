#!/bin/sh

set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work=$(mktemp -d /tmp/warp-watchdog-test.XXXXXX)
trap 'rm -rf "$work"' EXIT HUP INT TERM

mkdir -p "$work/bin" "$work/state" "$work/sys/warp" "$work/lock-parent"
: >"$work/sys/warp/tun_flags"

cat >"$work/uci" <<'EOF'
#!/bin/sh
case "$*" in
*warp.main.actual_interface*) printf '%s\n' warp ;;
*warp.main.actual_backend*) printf '%s\n' amneziawg ;;
*warp.main.latency_watchdog*) printf '%s\n' 1 ;;
*warp.main.latency_threshold*) printf '%s\n' 250 ;;
*warp.main.latency_cooldown*) printf '%s\n' 300 ;;
*) exit 1 ;;
esac
EOF

cat >"$work/awgctl" <<'EOF'
#!/bin/sh
case "$3" in
last_handshake_time_sec) date +%s ;;
rx_bytes) printf '%s\n' 100 ;;
*) exit 1 ;;
esac
EOF

cat >"$work/ping" <<'EOF'
#!/bin/sh
printf '%s\n' \
  '64 bytes from 1.1.1.1: seq=0 ttl=58 time=490.000 ms' \
  '64 bytes from 1.1.1.1: seq=1 ttl=58 time=500.000 ms' \
  '64 bytes from 1.1.1.1: seq=2 ttl=58 time=510.000 ms'
EOF

cat >"$work/manager" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>"$work/manager.calls"
printf '%s\n' '{"ok":true,"code":"endpoint_kept","interface":"warp"}'
EOF

cat >"$work/bin/sleep" <<EOF
#!/bin/sh
count=0
[ ! -r "$work/sleep.count" ] || count=\$(sed -n '1p' "$work/sleep.count")
count=\$((count + 1))
printf '%s\n' "\$count" >"$work/sleep.count"
[ "\$count" -lt 5 ]
EOF

cat >"$work/bin/logger" <<'EOF'
#!/bin/sh
exit 0
EOF

chmod 700 "$work/uci" "$work/awgctl" "$work/ping" "$work/manager" \
  "$work/bin/sleep" "$work/bin/logger"

PATH="$work/bin:$PATH" \
WARP_STATE_DIR="$work/state" \
WARP_LOCK_DIR="$work/lock-parent/manager.lock" \
WARP_UCI_BIN="$work/uci" \
WARP_AWG_CTL_BIN="$work/awgctl" \
WARP_MANAGER_BIN="$work/manager" \
WARP_PING_BIN="$work/ping" \
WARP_SYS_CLASS_NET="$work/sys" \
WARP_LATENCY_FILE="$work/latency" \
WARP_WATCHDOG_INTERVAL=0 \
WARP_WATCHDOG_RECOVERY_PAUSE=0 \
WARP_WATCHDOG_LATENCY_SAMPLES=3 \
WARP_WATCHDOG_LATENCY_FAILURES=3 \
  "$root_dir/root/usr/libexec/warp-watchdog"

grep -qx 'optimize 500' "$work/manager.calls"
awk '$3 == 500 { found = 1 } END { exit !found }' "$work/latency"
grep -q 'result=.*endpoint_kept' "$work/state/last_optimization"

printf '%s\n' 'Watchdog latency test: OK'
