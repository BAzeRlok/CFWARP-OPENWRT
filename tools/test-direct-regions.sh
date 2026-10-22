#!/bin/sh

set -eu
umask 077

REPOSITORY="BAzeRlok/CFWARP-OPENWRT"
RELEASE_TAG="v4.0.1"
RELEASE_URL="https://github.com/$REPOSITORY/releases/download/$RELEASE_TAG"
SCOUT_VERSION="0.16.0-r1"
ACCOUNT_FILE="/etc/warp/awg-account.json"
WAN_INTERFACE="${WARP_TEST_INTERFACE:-wan}"
WAN_DEVICE="${WARP_TEST_DEVICE:-}"
SAMPLE="${WARP_TEST_SAMPLE:-8}"
JOBS="${WARP_TEST_JOBS:-2}"
PING_COUNT="${WARP_TEST_PING_COUNT:-10}"
PING_TARGET="${WARP_TEST_PING_TARGET:-8.8.8.8}"
REGIONS="${WARP_TEST_REGIONS:-FI:HEL SE:ARN SE:GOT EE:TLL LV:RIX LT:VNO PL:WAW PL:WRO DE:FRA DE:TXL DE:MUC DE:DUS DE:HAM NL:AMS DK:CPH NO:OSL CZ:PRG AT:VIE RO:OTP BG:SOF TR:IST FR:CDG GB:LHR}"

die() {
	printf 'Ошибка: %s\n' "$*" >&2
	exit 1
}

download() {
	url=$1
	destination=$2
	if command -v curl >/dev/null 2>&1; then
		curl -fL --connect-timeout 15 --max-time 300 "$url" -o "$destination"
	elif command -v wget >/dev/null 2>&1; then
		wget -O "$destination" "$url"
	elif command -v uclient-fetch >/dev/null 2>&1; then
		uclient-fetch -O "$destination" "$url"
	else
		die 'не найден curl, wget или uclient-fetch'
	fi
}

valid_uint_range() {
	value=$1
	minimum=$2
	maximum=$3
	case "$value" in ''|*[!0-9]*) return 1 ;; esac
	[ "$value" -ge "$minimum" ] && [ "$value" -le "$maximum" ]
}

append_csv() {
	current=$1
	value=$2
	if [ -n "$current" ]; then
		printf '%s,%s' "$current" "$value"
	else
		printf '%s' "$value"
	fi
}

cleanup_done=0
was_up=false
temp_dir=

cleanup() {
	[ "$cleanup_done" -eq 0 ] || return 0
	cleanup_done=1
	trap - EXIT HUP INT TERM
	if [ -n "$temp_dir" ]; then
		case "$temp_dir" in
			/tmp/warp-region-test-bin.*) rm -rf -- "$temp_dir" ;;
		esac
	fi
	if [ "$was_up" = true ]; then
		printf '\nВосстановление исходного WARP...\n' >&2
		if ! /usr/libexec/warp-manager reconnect >&2; then
			printf '%s\n' 'Не удалось автоматически восстановить WARP. Выполните /usr/libexec/warp-manager reconnect' >&2
		fi
	fi
}

trap cleanup EXIT
trap 'exit 130' HUP INT TERM

[ "$(id -u)" -eq 0 ] || die 'запустите скрипт от root'
[ -r "$ACCOUNT_FILE" ] || die "не найден зарегистрированный аккаунт $ACCOUNT_FILE"
[ -x /usr/libexec/warp-manager ] || die 'не найден /usr/libexec/warp-manager'
command -v apk >/dev/null 2>&1 || die 'не найден apk'
command -v sha256sum >/dev/null 2>&1 || die 'не найден sha256sum'
command -v ubus >/dev/null 2>&1 || die 'не найден ubus'
command -v jsonfilter >/dev/null 2>&1 || die 'не найден jsonfilter'
valid_uint_range "$SAMPLE" 1 64 || die 'WARP_TEST_SAMPLE должен быть числом от 1 до 64'
valid_uint_range "$JOBS" 1 4 || die 'WARP_TEST_JOBS должен быть числом от 1 до 4'
valid_uint_range "$PING_COUNT" 3 30 || die 'WARP_TEST_PING_COUNT должен быть числом от 3 до 30'

if [ -z "$WAN_DEVICE" ]; then
	wan_status=$(ubus call "network.interface.$WAN_INTERFACE" status 2>/dev/null) ||
		die "логический интерфейс $WAN_INTERFACE недоступен; задайте WARP_TEST_INTERFACE или WARP_TEST_DEVICE"
	WAN_DEVICE=$(printf '%s\n' "$wan_status" | jsonfilter -e '@.l3_device' 2>/dev/null || true)
fi
[ -n "$WAN_DEVICE" ] || die 'не удалось определить L3-устройство WAN'
[ -e "/sys/class/net/$WAN_DEVICE" ] || die "сетевого устройства $WAN_DEVICE не существует"

countries=
nodes=
for pair in $REGIONS; do
	country=${pair%%:*}
	node=${pair#*:}
	printf '%s' "$country" | grep -Eq '^[A-Za-z]{2}$' || die "некорректная страна в $pair"
	printf '%s' "$node" | grep -Eq '^[A-Za-z]{3}$' || die "некорректный узел в $pair"
	country=$(printf '%s' "$country" | tr '[:lower:]' '[:upper:]')
	node=$(printf '%s' "$node" | tr '[:lower:]' '[:upper:]')
	countries=$(append_csv "$countries" "$country")
	nodes=$(append_csv "$nodes" "$node")
done
[ -n "$countries" ] && [ -n "$nodes" ] || die 'список WARP_TEST_REGIONS пуст'

architecture=$(sed -n 's/^DISTRIB_ARCH=//p' /etc/openwrt_release 2>/dev/null | sed -n '1p' | tr -d "'\"")
if [ -z "$architecture" ]; then
	architecture=$(apk --print-arch 2>/dev/null | sed -n '1p')
fi
case "$architecture" in
	aarch64)
	scout_sha='d852382a566dbe939ff06055738dfa0a894dd323929bfc38a32e620f38b45cd7'
	;;
	aarch64_cortex-a53)
	scout_sha='da4fd0e15b38ac9b751f7b7e2ffe9caed341d3c6818c86de52780362f7b59fd9'
	;;
	*) die "архитектура $architecture не поддерживается" ;;
esac

temp_dir=$(mktemp -d /tmp/warp-region-test-bin.XXXXXX) || die 'не удалось создать временный каталог'
package="warp-warpscout-$SCOUT_VERSION-$architecture.apk"
download "$RELEASE_URL/$package" "$temp_dir/$package"
actual_sha=$(sha256sum "$temp_dir/$package" | awk '{ print $1 }')
[ "$actual_sha" = "$scout_sha" ] || die 'не совпала SHA-256 временного сканера'
apk --allow-untrusted extract --destination "$temp_dir/root" "$temp_dir/$package" >/dev/null
scout="$temp_dir/root/usr/libexec/warp-warpscout"
[ -x "$scout" ] || die 'в APK не найден исполняемый файл WARPSCOUT'

port=$(sed -n 's/^[[:space:]]*Endpoint[[:space:]]*=[[:space:]]*[0-9.]*:\([0-9][0-9]*\)[[:space:]]*$/\1/p' /etc/warp/awg.conf 2>/dev/null | sed -n '1p')
[ -n "$port" ] || port=2408
valid_uint_range "$port" 1 65535 || die "некорректный UDP-порт $port"
sni=$(uci -q get warp.main.sni 2>/dev/null || true)
[ -n "$sni" ] || sni=ozon.ru

result_dir="/tmp/warp-region-test.$(date +%s)"
mkdir "$result_dir" || die 'не удалось создать каталог результатов'
chmod 700 "$result_dir"
report="$result_dir/report.txt"

status=$(/usr/libexec/warp-manager status 2>/dev/null || true)
case "$status" in *'"up":true'*) was_up=true ;; esac

printf '%s\n' 'Проверка не меняет UCI и сохранённый endpoint.'
printf 'На время теста WARP будет остановлен и затем восстановлен: %s\n' "$was_up"
printf 'Прямой интерфейс: %s (%s)\n' "$WAN_INTERFACE" "$WAN_DEVICE"
printf 'Комбинации: %s\n' "$REGIONS"
printf 'Выборка: %s адресов на подсеть, workers=%s, UDP-порт=%s\n' "$SAMPLE" "$JOBS" "$port"
printf 'Результаты: %s\n\n' "$result_dir"

if command -v curl >/dev/null 2>&1; then
	printf '%s\n' '=== Прямой выход WAN до теста ==='
	curl -4 --interface "$WAN_DEVICE" -sS --connect-timeout 10 --max-time 20 \
		https://1.1.1.1/cdn-cgi/trace 2>/dev/null |
		grep -E '^(ip|colo|loc|warp)=' || true
	printf '\n'
fi

if [ "$was_up" = true ]; then
	printf '%s\n' 'Остановка текущего WARP на время проверки...'
	/etc/init.d/warp-watchdog stop >/dev/null 2>&1 || true
	/etc/init.d/warp stop >/dev/null 2>&1 || true
fi

printf '%s\n' '=== Поиск иностранных узлов напрямую через WAN ==='
printf '%s\n' 'Иностранный NODE сам по себе недостаточен: смотрите столбец SEEN AS.'

if GOMEMLIMIT=64MiB GOMAXPROCS=1 NO_COLOR=1 "$scout" scan --plain \
	--account "$ACCOUNT_FILE" \
	--proto awg \
	--gen-i1 quic \
	--i1-sni "$sni" \
	--interface "$WAN_DEVICE" \
	--port "$port" \
	--sample "$SAMPLE" \
	--tunnel-jobs "$JOBS" \
	--tun-ping-count "$PING_COUNT" \
	--ping-target "$PING_TARGET" \
	--country "$countries" \
	--node "$nodes" \
	--output "$report"; then
	printf '\n%s\n' '=== Краткий итог ==='
	sed -n '1p' "$report"
	awk '
		/^# [0-9]+ torn down/ { exit }
		$1 ~ /^[0-9]+\./ { seen[$5] = 1; nodes[$6] = 1 }
		END {
			printf "SEEN AS:"
			for (value in seen) printf " %s", value
			printf "\nNODE:"
			for (value in nodes) printf " %s", value
			printf "\n"
		}
	' "$report"
	printf 'Полный отчёт: %s\n' "$report"
	working=$(sed -n 's/^# WARP endpoints: \([0-9][0-9]*\) working.*/\1/p' "$report")
	case "$working" in ''|*[!0-9]*) working=0 ;; esac
	if [ "$working" -gt 0 ]; then
		printf '%s\n' 'Код SEEN AS, отличный от RU, означает найденный стабильный прямой зарубежный выход.'
	else
		printf '%s\n' 'Стабильных иностранных endpoints нет; строки torn down рабочими не считаются.'
		exit 3
	fi
else
	printf '\n%s\n' 'Подходящих иностранных узлов в этой выборке не найдено.'
	printf '%s\n' 'Для более глубокой проверки повторите с WARP_TEST_SAMPLE=24.'
	exit 2
fi
