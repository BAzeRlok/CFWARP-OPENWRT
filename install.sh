#!/bin/sh

set -eu
umask 077

REPOSITORY="BAzeRlok/CFWARP-OPENWRT"
RELEASE_TAG="v1.4.0"
RELEASE_URL="https://github.com/$REPOSITORY/releases/download/$RELEASE_TAG"
USQUE_PACKAGE="warp-usque-2.0.1-r3.apk"
LUCI_PACKAGE="luci-app-warp-1.4.0-r1.apk"
I18N_PACKAGE="luci-i18n-warp-ru-0.260901.59252.apk"
TRANSPORT="${WARP_TRANSPORT:-quic}"
MASQUE_SNI="${WARP_SNI:-ozon.ru}"

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

case "$TRANSPORT" in
	quic|http2) ;;
	*) die 'WARP_TRANSPORT должен быть quic или http2' ;;
esac

ARCH=$(apk --print-arch 2>/dev/null | sed -n '1p')
[ "$ARCH" = aarch64_cortex-a53 ] || die "для архитектуры $ARCH пока нет warp-usque; требуется aarch64_cortex-a53"

INSTALL_DIR=$(mktemp -d /tmp/cfwarp-install.XXXXXX) || die 'не удалось создать временный каталог'
trap 'rm -rf "$INSTALL_DIR"' EXIT HUP INT TERM

printf 'Скачивание CFWARP %s для %s...\n' "$RELEASE_TAG" "$ARCH"
download "$RELEASE_URL/SHA256SUMS" "$INSTALL_DIR/SHA256SUMS"
for package in "$USQUE_PACKAGE" "$LUCI_PACKAGE" "$I18N_PACKAGE"; do
	download "$RELEASE_URL/$package" "$INSTALL_DIR/$package"
	verify_file "$INSTALL_DIR/$package"
done

printf 'Установка проверенных пакетов...\n'
apk add --allow-untrusted \
	"$INSTALL_DIR/$USQUE_PACKAGE" \
	"$INSTALL_DIR/$LUCI_PACKAGE" \
	"$INSTALL_DIR/$I18N_PACKAGE"

uci set warp.main.masque_transport="$TRANSPORT"
uci set warp.main.masque_sni="$MASQUE_SNI"
uci commit warp

/etc/init.d/rpcd restart
rm -f /tmp/luci-indexcache.*
rm -rf /tmp/luci-modulecache/

printf 'Регистрация и запуск WARP...\n'
result=$(/usr/libexec/warp-manager reconnect)
printf '%s\n' "$result"
case "$result" in
	*'"ok":true'*) ;;
	*)
		printf '%s\n' 'Диагностика: /etc/warp/backend.log и logread -e warp-usque -e warp' >&2
		exit 1
		;;
esac

/usr/libexec/warp-manager status
printf '%s\n' 'Установка завершена. Пакет не меняет маршруты и firewall автоматически.'
