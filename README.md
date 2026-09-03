# CFWARP for OpenWrt

LuCI-приложение регистрирует Cloudflare WARP и создаёт отдельный интерфейс
`warp` через MASQUE over QUIC. Пакет не меняет маршруты, DNS и firewall:
трафик пойдёт через WARP только после настройки маршрутизации пользователем.

## Установка

Поддерживаются `aarch64` и `aarch64_cortex-a53` на OpenWrt с `apk`:

```sh
wget -qO- https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh | sh
```

Другой маскирующий SNI можно указать при установке:

```sh
wget -qO- https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh |
WARP_SNI=www.apple.com sh
```

Установщик скачивает релиз `v2.0.1`, проверяет SHA-256, устанавливает
`warp-usque` 4.2.1-r9 и `luci-app-warp` 2.0.1-r1, затем регистрирует и
запускает WARP.

## Использование

Откройте LuCI → Сеть → Cloudflare WARP. Интерфейс можно подключить,
переподключить, отключить или полностью удалить регистрацию.

Основные настройки:

- имя интерфейса — по умолчанию `warp`;
- MTU — по умолчанию `1280`;
- keepalive — по умолчанию `5` секунд;
- IPv4 и IPv6 внутри туннеля;
- SNI для маскировки QUIC.

Проверка состояния:

```sh
/usr/libexec/warp-manager status
ip -s link show dev warp
logread -e warp-usque -e warp
```

Проверка выхода через WARP без изменения основного маршрута:

```sh
(
    ip route add 1.1.1.1/32 dev warp || exit 1
    trap 'ip route del 1.1.1.1/32 dev warp' EXIT INT TERM

    curl -4 --connect-timeout 10 --max-time 20 \
        https://1.1.1.1/cdn-cgi/trace |
        grep -E '^(ip|colo|warp|gateway)='
)
```

Ожидаемое значение: `warp=on`.

## Безопасность и совместимость

- приватный ключ хранится в `/etc/warp/usque.json` с правами `0600`;
- endpoint проверяется по закреплённому публичному ключу Cloudflare;
- SACK-подтверждения повторяются только при обнаруженной потере TCP-сегмента,
  сокращая ожидание повторной передачи на нестабильном QUIC-канале;
- backend изолирован от установленного пользователем sing-box;
- WireGuard, Zapret, PBR и firewall пакет автоматически не настраивает;
- туннельный транспорт только один: MASQUE over QUIC.

## Сборка

Корневой каталог собирает LuCI-пакет. Подкаталог `warp-usque/` содержит
OpenWrt-рецепт отдельного QUIC-only backend. Статические проверки:

```sh
python3 -m unittest -v tests.test_static
```

Актуальные APK и `SHA256SUMS` находятся в
[последнем GitHub Release](https://github.com/BAzeRlok/CFWARP-OPENWRT/releases/latest).
