#!/bin/sh

# Run only on a disposable OpenWrt 25.12.5 test router.
set -eu

manager=/usr/libexec/warp-manager
work=/tmp/luci-app-warp-integration
phase=${1:-full}

[ "$(id -u)" -eq 0 ] || { echo 'Run as root' >&2; exit 1; }
[ -x "$manager" ] || { echo 'luci-app-warp is not installed' >&2; exit 1; }
[ "${WARP_ACCEPT_CLOUDFLARE_TERMS:-}" = YES ] || {
	echo 'Set WARP_ACCEPT_CLOUDFLARE_TERMS=YES after reviewing Cloudflare terms.' >&2
	exit 1
}

mkdir -p "$work"
chmod 700 "$work"

snapshot() {
	ip route show >"$work/routes4.$1"
	ip -6 route show >"$work/routes6.$1"
	uci export firewall >"$work/firewall.$1"
	uci export dhcp >"$work/dhcp.$1"
	cp /tmp/resolv.conf.d/resolv.conf.auto "$work/resolv.$1" 2>/dev/null || : >"$work/resolv.$1"
}

assert_unchanged() {
	diff -u "$work/routes4.before" "$work/routes4.$1"
	diff -u "$work/routes6.before" "$work/routes6.$1"
	diff -u "$work/firewall.before" "$work/firewall.$1"
	diff -u "$work/dhcp.before" "$work/dhcp.$1"
	diff -u "$work/resolv.before" "$work/resolv.$1"
}

assert_managed_config() {
	name=$(uci -q get warp.main.actual_interface)
	backend=$(uci -q get warp.main.actual_backend)
	[ -n "$name" ] && [ -n "$backend" ]
	[ "$(uci -q get network.$name.warp_managed)" = luci-app-warp ]
	[ "$(uci -q get network.$name.warp_backend)" = "$backend" ]
	[ "$(uci -q get network.$name.proto)" = none ]
	[ "$(uci -q get network.$name.device)" = "$name" ]
	[ "$(uci -q get network.$name.defaultroute)" = 0 ]
	[ "$(uci -q get network.$name.peerdns)" = 0 ]
	[ -r "/sys/class/net/$name/tun_flags" ]
	[ "$backend" = amneziawg ]
	[ -s /etc/warp/awg-account.json ]
	[ -s /etc/warp/awg.conf ]
	[ "$(stat -c '%a' /etc/warp/awg-account.json)" = 600 ]
	[ "$(stat -c '%a' /etc/warp/awg.conf)" = 600 ]
	/usr/libexec/warp-awgctl get "$name" endpoint | grep -Eq '^[0-9.]+:[0-9]+$'
	! uci show firewall | grep -Eq "(^|[.='])${name}([.=' ]|$)"
}

case "$phase" in
	pre-reboot)
		snapshot before-reboot
		sha256sum /etc/warp/awg-account.json >"$work/registration.sha256"
		echo 'Snapshots saved. Reboot, then run this script with post-reboot.'
		exit 0
		;;
	post-reboot)
		sha256sum -c "$work/registration.sha256"
		assert_managed_config
		name=$(uci -q get warp.main.actual_interface)
		ubus call "network.interface.$name" status | jsonfilter -e '@.up' | grep -qx true
		echo 'Reboot persistence test: OK (registration unchanged and AWG TUN up).'
		exit 0
		;;
	full) ;;
	*) echo 'Usage: router-integration.sh [full|pre-reboot|post-reboot]' >&2; exit 2 ;;
esac

# Exercise name-conflict handling without touching a pre-existing section. On a
# disposable clean router, create a harmless foreign UCI section before taking
# the baseline and verify that the manager preserves it byte-for-byte.
conflict_created=0
if uci -q get network.warp >/dev/null 2>&1; then
	[ "$(uci -q get network.warp.warp_managed || true)" != luci-app-warp ] || {
		echo 'Remove the previous WARP registration before the full test.' >&2
		exit 1
	}
else
	uci -q batch <<'EOF'
set network.warp=interface
set network.warp.proto='none'
set network.warp.warp_test_fixture='1'
EOF
	uci -q commit network
	conflict_created=1
fi
uci -q show network.warp >"$work/conflict.before"

snapshot before
firewall_hash=$(sha256sum "$work/firewall.before" | cut -d' ' -f1)

result=$($manager register)
printf '%s' "$result" | jsonfilter -e '@.ok' | grep -qx true
assert_managed_config
name=$(uci -q get warp.main.actual_interface)
[ "$name" != warp ]
curl -4 --interface "$name" --connect-timeout 10 --max-time 20 \
	https://1.1.1.1/cdn-cgi/trace | grep -qx 'warp=on'
uci -q show network.warp >"$work/conflict.connected"
diff -u "$work/conflict.before" "$work/conflict.connected"
snapshot connected
assert_unchanged connected

registration_hash=$(sha256sum /etc/warp/awg-account.json | cut -d' ' -f1)
$manager register >/dev/null
[ "$registration_hash" = "$(sha256sum /etc/warp/awg-account.json | cut -d' ' -f1)" ]

$manager disable >/dev/null
[ -e /etc/warp/disabled ]
$manager reconnect >/dev/null
[ ! -e /etc/warp/disabled ]
snapshot reconnected
assert_unchanged reconnected

private_key=$(jsonfilter -i /etc/warp/awg-account.json -e '@.private_key')
token=$(jsonfilter -i /etc/warp/awg-account.json -e '@.token')
! $manager status | grep -F "$private_key"
! ubus call luci.warp status | grep -F "$token"
! logread | grep -F "$private_key"
! logread | grep -F "$token"

rm -f /tmp/warp-rpc-injection
ubus call luci.warp status '{"command":"touch /tmp/warp-rpc-injection"}' >/dev/null
[ ! -e /tmp/warp-rpc-injection ]

$manager unregister >/dev/null
[ ! -e /etc/warp/awg-account.json ]
[ ! -e /etc/warp/awg.conf ]
[ ! -e /etc/warp/backend.log ]
uci -q show network.warp >"$work/conflict.unregistered"
diff -u "$work/conflict.before" "$work/conflict.unregistered"
snapshot unregistered
assert_unchanged unregistered
[ "$firewall_hash" = "$(sha256sum "$work/firewall.unregistered" | cut -d' ' -f1)" ]

if [ "$conflict_created" -eq 1 ]; then
	uci -q delete network.warp
	uci -q commit network
	ubus call network reload '{}' >/dev/null
fi

echo 'Router integration tests: OK'
