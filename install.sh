#!/bin/sh

set -eu
umask 077

REPOSITORY="BAzeRlok/CFWARP-OPENWRT"
RELEASE_TAG="v4.2.0"
RELEASE_URL="https://github.com/$REPOSITORY/releases/download/$RELEASE_TAG"
LUCI_PACKAGE="luci-app-warp-4.2.0-r3.apk"
I18N_PACKAGE="luci-i18n-warp-ru-4.2.0.apk"
SNI_SET="${WARP_SNI+x}"
NODE_COUNTRIES_SET="${WARP_NODE_COUNTRIES+x}"
NODES_SET="${WARP_NODES+x}"
EXCLUDE_NODE_COUNTRIES_SET="${WARP_EXCLUDE_NODE_COUNTRIES+x}${WARP_EXCLUDE_COUNTRIES+x}"
EXCLUDE_NODES_SET="${WARP_EXCLUDE_NODES+x}"
UPSTREAM_INTERFACE_SET="${WARP_UPSTREAM_INTERFACE+x}"
MASKING_SNI="${WARP_SNI:-}"
NODE_COUNTRIES="${WARP_NODE_COUNTRIES:-}"
NODES="${WARP_NODES:-}"
EXCLUDE_NODE_COUNTRIES="${WARP_EXCLUDE_NODE_COUNTRIES:-${WARP_EXCLUDE_COUNTRIES:-}}"
EXCLUDE_NODES="${WARP_EXCLUDE_NODES:-}"
UPSTREAM_INTERFACE="${WARP_UPSTREAM_INTERFACE:-}"

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

verify_file() {
	file=$1
	expected=$(awk -v name="${file##*/}" '$2 == name { print $1; exit }' "$INSTALL_DIR/SHA256SUMS")
	[ -n "$expected" ] || die "нет контрольной суммы для ${file##*/}"
	actual=$(sha256sum "$file" | awk '{ print $1 }')
	[ "$actual" = "$expected" ] || die "не совпала SHA-256 для ${file##*/}"
}

[ "$(id -u)" -eq 0 ] || die 'запустите установщик от root'
command -v apk >/dev/null 2>&1 || die 'поддерживается только OpenWrt с apk'
command -v uci >/dev/null 2>&1 || die 'не найден UCI'

ARCH=$(sed -n 's/^DISTRIB_ARCH=//p' /etc/openwrt_release 2>/dev/null | sed -n '1p' | tr -d "'\"")
if [ -z "$ARCH" ]; then
	ARCH=$(sed -n 's/^OPENWRT_ARCH=//p' /usr/lib/os-release 2>/dev/null | sed -n '1p' | tr -d "'\"")
fi
[ -n "$ARCH" ] || ARCH=$(apk --print-arch 2>/dev/null | sed -n '1p')
case "$ARCH" in
	aarch64|aarch64_cortex-a53)
		AWG_PACKAGE="warp-awg-3.1.20260828-r3-$ARCH.apk"
		;;
	*)
		die "для архитектуры $ARCH пока нет AWG backend; поддерживаются aarch64 и aarch64_cortex-a53"
		;;
esac

INSTALL_DIR=$(mktemp -d /tmp/cfwarp-install.XXXXXX) || die 'не удалось создать временный каталог'
trap 'rm -rf "$INSTALL_DIR"' EXIT HUP INT TERM

printf 'Скачивание WARP %s для %s...\n' "$RELEASE_TAG" "$ARCH"
download "$RELEASE_URL/SHA256SUMS" "$INSTALL_DIR/SHA256SUMS"
for package in "$AWG_PACKAGE" "$LUCI_PACKAGE" "$I18N_PACKAGE"; do
	download "$RELEASE_URL/$package" "$INSTALL_DIR/$package"
	verify_file "$INSTALL_DIR/$package"
done

printf 'Установка проверенных пакетов...\n'
apk add --allow-untrusted \
	"$INSTALL_DIR/$AWG_PACKAGE" \
	"$INSTALL_DIR/$LUCI_PACKAGE" \
	"$INSTALL_DIR/$I18N_PACKAGE"

[ "$SNI_SET" = x ] || MASKING_SNI=$(uci -q get warp.main.sni 2>/dev/null || true)
[ -n "$MASKING_SNI" ] || MASKING_SNI=ozon.ru
if [ "$NODE_COUNTRIES_SET" != x ]; then
	NODE_COUNTRIES=$(uci -q get warp.main.node_countries 2>/dev/null || true)
fi
if [ "$NODES_SET" != x ]; then
	NODES=$(uci -q get warp.main.nodes 2>/dev/null || true)
fi
if [ -z "$EXCLUDE_NODE_COUNTRIES_SET" ]; then
	if EXCLUDE_NODE_COUNTRIES=$(uci -q get warp.main.exclude_node_countries 2>/dev/null); then
		:
	elif EXCLUDE_NODE_COUNTRIES=$(uci -q get warp.main.exclude_countries 2>/dev/null); then
		:
	else
		EXCLUDE_NODE_COUNTRIES=RU
	fi
fi
if [ "$EXCLUDE_NODES_SET" != x ]; then
	if EXCLUDE_NODES=$(uci -q get warp.main.exclude_nodes 2>/dev/null); then
		:
	else
		EXCLUDE_NODES=DME
	fi
fi
if [ "$UPSTREAM_INTERFACE_SET" != x ]; then
	UPSTREAM_INTERFACE=$(uci -q get warp.main.upstream_interface 2>/dev/null || true)
fi

uci set warp.main.sni="$MASKING_SNI"
uci set warp.main.node_countries="$NODE_COUNTRIES"
uci set warp.main.nodes="$NODES"
uci set warp.main.exclude_node_countries="$EXCLUDE_NODE_COUNTRIES"
uci set warp.main.exclude_nodes="$EXCLUDE_NODES"
uci set warp.main.upstream_interface="$UPSTREAM_INTERFACE"
uci -q delete warp.main.exclude_countries 2>/dev/null || true
uci commit warp

# WARPSCOUT is downloaded into /tmp only when registration or an explicit
# endpoint search needs it. Remove the persistent copy left by older releases.
if apk info -e warp-warpscout >/dev/null 2>&1; then
	apk del warp-warpscout
fi

/etc/init.d/rpcd restart
rm -f /tmp/luci-indexcache.*
rm -rf /tmp/luci-modulecache/

printf 'Регистрация и запуск WARP...\n'
result=$(/usr/libexec/warp-manager enable)
printf '%s\n' "$result"
case "$result" in
	*'"ok":true'*) ;;
	*)
		printf '%s\n' 'Диагностика: /etc/warp/backend.log и logread -e warp-awg -e warp-watchdog -e warp' >&2
		exit 1
		;;
esac

/usr/libexec/warp-manager status
printf '%s\n' 'Установка завершена. DNS и firewall не изменяются; маршрут создаётся только для endpoint при заданном upstream-интерфейсе.'
