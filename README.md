# CFWARP for OpenWrt

LuCI-приложение для OpenWrt 25.12.x, которое регистрирует бесплатное устройство
Cloudflare WARP и создаёт отдельный userspace-интерфейс через MASQUE backend
`warp-usque`.

Приложение намеренно не направляет пользовательский трафик через WARP. Оно не
создаёт firewall-зону, forwarding, NAT, policy routing, DNS-настройки, default
route или kill switch. Выбор трафика для интерфейса `warp` остаётся за
пользователем или отдельным PBR-пакетом.

## Установка одной командой

Установщик рассчитан на чистую OpenWrt 25.12.x с архитектурой `aarch64` или
`aarch64_cortex-a53`:

```sh
wget -qO- https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh | sh
```

Он скачивает APK из GitHub Release `v1.7.4`, автоматически выбирает вариант
backend по пакетной архитектуре `DISTRIB_ARCH`, проверяет SHA-256, устанавливает
пакеты и запускает регистрацию. По умолчанию используется QUIC/HTTP3 с
keepalive 5 секунд и маскирующим SNI `ozon.ru`. HTTP/2 использует канонический
SNI Cloudflare и собственный совместимый CONNECT-IP transport. Для адаптивного
режима QUIC → HTTP/2:

```sh
wget -qO- https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh | WARP_TRANSPORT=auto sh
```

Перед запуском скрипт можно сохранить и проверить:

```sh
wget -O /tmp/cfwarp-install.sh https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh
sed -n '1,240p' /tmp/cfwarp-install.sh
sh /tmp/cfwarp-install.sh
```

## Как устроено подключение

Основной backend собирается из закреплённого `usque` 4.2.1 и устанавливается
как `/usr/libexec/warp-usque`. Локальный патч:

- использует Chrome-подобный ClientHello для API Cloudflare;
- делит ClientHello на две корректные TLS-записи внутри SNI;
- принудительно использует IPv4 для регистрации;
- корректно фиксирует ALPN `http/1.1` для служебного HTTP-клиента;
- поддерживает QUIC/HTTP3 и TCP/HTTP2 для MASQUE;
- устанавливает HTTP/2 через проверенный на WARP plain CONNECT и передаёт IP-
  пакеты в Capsule Protocol DATA frames без высокоуровневого `http.Client`;
- использует приватный plain CONNECT Cloudflare с
  `cf-connect-proto: cf-connect-ip` и `capsule-protocol: ?1`, а также сохраняет
  `TCP_NODELAY` для малых DATA frames;
- перед объявлением HTTP/2-туннеля рабочим проверяет его несколькими IP-пакетами;
  если CONNECT успешен, но data plane молчит или отвечает ICMP-отказом,
  параллельно выбирает рабочую пару адрес/порт из обоих пулов Cloudflare;
- отдельно обрабатывает HTTP/2 SETTINGS, flow control, PING, GOAWAY и
  `RST_STREAM`, чтобы соединение не превращалось в молчащий data plane;
- для HTTP/2 всегда использует канонический SNI
  `consumer-masque.cloudflareclient.com`, а пользовательский маскирующий SNI
  оставляет только для QUIC/HTTP3;
- принимает HTTP/2 endpoint как с ALPN `h2`, так и с пустым ALPN в режиме
  prior knowledge, который Cloudflare использует на части соединений;
- в адаптивном режиме переключается на HTTP/2 только после трёх подряд ошибок
  установления QUIC, но не после обычного idle-разрыва рабочего туннеля;
- использует TCP keepalive без несовместимых с Cloudflare HTTP/2 PING-таймеров;
- сохраняет один ожидающий исходящий пакет и единственный TUN reader между
  reconnect, не создавая дополнительную длинную FIFO-очередь перед QUIC;
- сохраняет проверку сертификата API и проверку endpoint по публичному ключу.

Backend работает автономно и не читает конфигурацию сторонних средств обхода,
не управляет их процессами или nftables-правилами.

## Возможности

- LuCI: `Сеть → Cloudflare WARP`;
- фиксированные RPC-методы без выполнения произвольных команд;
- регистрация и хранение профиля в `/etc/warp/usque.json` с правами `0600`;
- отдельный TUN-интерфейс, видимый в `Сеть → Интерфейсы`;
- `defaultroute=0`, `peerdns=0`, отсутствие auto-route и firewall-зоны;
- безопасный выбор `cfwarp`, если имя `warp` уже занято;
- автоматический перезапуск backend через procd;
- повторное подключение без создания новой регистрации.

## Состав

- `luci-app-warp` — LuCI, RPC, менеджер и procd-служба;
- `luci-i18n-warp-ru` — русский перевод;
- `warp-usque` — архитектурно-зависимый MASQUE backend;
- `kmod-tun`, `curl`, `ca-bundle`, `jsonfilter` — системные зависимости.

## Ручная установка

Скачайте из GitHub Release `v1.7.4` файл `SHA256SUMS`, два LuCI-пакета и
вариант `warp-usque`, соответствующий `DISTRIB_ARCH` из `/etc/openwrt_release`.

```sh
ARCH="$(sed -n 's/^DISTRIB_ARCH=//p' /etc/openwrt_release | tr -d "'\"")"
USQUE_PACKAGE="warp-usque-4.2.1-r7-$ARCH.apk"
for PACKAGE in \
    "$USQUE_PACKAGE" \
    luci-app-warp-1.7.4-r1.apk \
    luci-i18n-warp-ru-26.245.65813.bad68c5.apk
do
    EXPECTED="$(awk -v name="$PACKAGE" '$2 == name { print $1; exit }' SHA256SUMS)"
    ACTUAL="$(sha256sum "$PACKAGE" | awk '{ print $1 }')"
    [ -n "$EXPECTED" ] && [ "$ACTUAL" = "$EXPECTED" ] || exit 1
done

apk add --allow-untrusted \
    "./$USQUE_PACKAGE" \
    ./luci-app-warp-1.7.4-r1.apk \
    ./luci-i18n-warp-ru-26.245.65813.bad68c5.apk

uci set warp.main.masque_transport='quic'
uci set warp.main.masque_sni='ozon.ru'
uci commit warp

/etc/init.d/rpcd restart
/usr/libexec/warp-manager reconnect
```

Перезагрузка роутера не требуется.

## Проверка

```sh
/usr/libexec/warp-manager status
ip -details link show warp
ip -s link show warp
ubus call network.interface.warp status
ubus call service list '{"name":"warp"}'
logread -e warp-usque -e warp
sed -n '1,120p' /etc/warp/backend.log
```

Одноразовый тест трафика через интерфейс:

```sh
(
    ip route add 1.1.1.1/32 dev warp || exit 1
    trap 'ip route del 1.1.1.1/32 dev warp' EXIT INT TERM
    curl -4 --connect-timeout 10 --max-time 20 \
        https://1.1.1.1/cdn-cgi/trace | grep -E '^(ip|colo|warp|gateway)='
)
```

Ожидается `warp=on`. Постоянный маршрут приложение не создаёт.

## Настройки UCI

```text
config warp 'main'
        option auto_start '1'
        option interface 'warp'
        option mtu '1280'
        option keepalive '5'
        option ipv4 '1'
        option ipv6 '1'
        option masque_transport 'quic'
        option masque_sni 'ozon.ru'
```

`masque_transport` принимает `auto`, `quic` или `http2`. В режиме `auto`
backend начинает с QUIC и переходит на HTTP/2 только после трёх последовательных
ошибок установления QUIC. Разрыв уже работавшего QUIC-туннеля вызывает быстрое
переподключение тем же транспортом. Поле `masque_sni` содержит DNS-имя без схемы
и порта и применяется только к QUIC/HTTP3. HTTP/2 всегда использует канонический
SNI Cloudflare, необходимый для стабильного data plane.

## Удаление

Сначала удалите устройство Cloudflare и локальный профиль:

```sh
/usr/libexec/warp-manager unregister
/etc/init.d/warp stop
/etc/init.d/warp disable
apk del luci-i18n-warp-ru luci-app-warp warp-usque
```

Если API Cloudflare недоступен, локальный профиль можно удалить вручную, но
устройство останется зарегистрированным на стороне Cloudflare.

## Сборка

Используйте официальный OpenWrt SDK, соответствующий target роутера:

```sh
./scripts/feeds update -a
./scripts/feeds install -a
ln -s /полный/путь/к/CFWarp-openwrt package/luci-app-warp
ln -s /полный/путь/к/CFWarp-openwrt/warp-usque package/warp-usque
make defconfig
make package/warp-usque/compile V=s
make package/luci-app-warp/compile V=s
```

LuCI-пакеты имеют архитектуру `noarch`; `warp-usque` необходимо собирать под
конкретную архитектуру OpenWrt.

## Тесты

```sh
./tests/run.sh
```

Интеграционный сценарий для одноразового тестового роутера находится в
`tests/router-integration.sh`.

## Лицензия

Apache-2.0. Исходный проект `usque` распространяется по лицензии MIT.
