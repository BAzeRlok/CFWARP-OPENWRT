# WARP для OpenWrt

Легковесное LuCI-приложение регистрирует Cloudflare WARP и создаёт отдельный
интерфейс `warp` через userspace AmneziaWG. Оно не меняет DNS, firewall и
маршрутизацию пользовательского трафика: интерфейс нужно самостоятельно выбрать
в Forkop, PBR или другой системе маршрутизации.

Постоянно устанавливаются только WARP, LuCI-перевод и AWG-бэкенд. Минимальный
неинтерактивный помощник регистрации и проверки AWG endpoint скачивается в
`/tmp`, проверяется по SHA-256 и удаляется сразу после первичной регистрации или
явного поиска другого endpoint. Поэтому его 9,2 МБ не занимают flash.

## Установка

Поддерживаются `aarch64` и `aarch64_cortex-a53` на OpenWrt с `apk`:

```sh
wget -qO- https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh | sh
```

Необязательные параметры можно передать установщику:

```sh
wget -qO- https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh |
WARP_SNI=ozon.ru \
WARP_NODE_COUNTRIES=NL \
WARP_NODES=AMS \
WARP_EXCLUDE_NODE_COUNTRIES=RU \
WARP_EXCLUDE_NODES=DME \
WARP_UPSTREAM_INTERFACE=foreign_awg sh
```

Все скачанные APK и временный сканер проверяются по опубликованным SHA-256.
Повторная установка сохраняет существующие настройки, если соответствующая
переменная окружения не задана. Оставшийся от старой версии пакет
`warp-warpscout` удаляется автоматически.

## Управление

Откройте LuCI → Сеть → WARP.

- «Переподключить» быстро перезапускает текущий endpoint и не выполняет поиск.
- «Найти другой endpoint» скачивает временный сканер, проверяет кандидатов
  реальным трафиком и сохраняет лучший.
- Watchdog после пяти неудачных проверок перезапускает текущий endpoint без
  сканирования и скачивания вспомогательных компонентов.

Основные настройки:

- интерфейс — `warp`;
- MTU — `1280`;
- Persistent Keepalive — `25` секунд;
- маскирующее имя — используется только в первом AWG-пакете;
- требуемые и исключаемые страны — страны входных узлов Cloudflare;
- требуемые и исключаемые узлы — трёхбуквенные коды, например `AMS` или `DME`;
- вышестоящий интерфейс — существующий зарубежный туннель для подключения к
  WARP endpoint.

Важно: узел `AMS` или страна узла `NL` не гарантируют нидерландский публичный IP.
Cloudflare использует anycast и обычно выдаёт WARP-выход около местоположения,
с которого установлена сессия. Для этого можно сначала поднять собственный AWG
или VPN до Нидерландов и указать его логическое имя OpenWrt как вышестоящий
интерфейс. WARP добавит только точечный маршрут к своему IPv4 endpoint через этот
туннель и удалит его при остановке. Если нужен строго гарантированный
нидерландский IP, надёжнее направлять нужный трафик непосредственно в собственный
нидерландский VPN, без второго слоя WARP.

Проверка состояния:

```sh
/usr/libexec/warp-manager status
ip -s link show dev warp
logread -e warp-awg -e warp-watchdog -e warp
```

В статусе отображаются endpoint, наблюдаемый узел Cloudflare и страна выхода,
если фоновая проверка завершилась. Проверить выход вручную без изменения
основного маршрута:

```sh
curl -4 --interface warp --connect-timeout 10 --max-time 20 \
    https://1.1.1.1/cdn-cgi/trace |
    grep -E '^(ip|colo|loc|warp|gateway)='
```

Ожидаемое значение — `warp=on`.

Проверить, доступны ли напрямую через WAN ближайшие зарубежные узлы Cloudflare,
не меняя текущую конфигурацию WARP:

```sh
wget -qO /tmp/warp-region-test.sh \
    https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/tools/test-direct-regions.sh
sh /tmp/warp-region-test.sh
```

Скрипт временно останавливает работающий WARP и проверяет одной выборкой
ближайшие узлы Северной, Центральной и Западной Европы строго через WAN, после
чего восстанавливает исходный туннель. В список входят, среди прочих, `FI/HEL`,
`SE/ARN`, `EE/TLL`, `LV/RIX`, `LT/VNO`, `PL/WAW`, `DE/FRA` и `NL/AMS`. UCI и
сохранённый endpoint не изменяются. Для более глубокой проверки:

```sh
WARP_TEST_SAMPLE=24 sh /tmp/warp-region-test.sh
```

Иностранный `NODE` ещё не означает иностранный публичный адрес: окончательный
результат определяется столбцом `SEEN AS` в отчёте.

Команды управления:

```sh
/usr/libexec/warp-manager reconnect  # быстрый перезапуск текущего endpoint
/usr/libexec/warp-manager rescan     # найти и применить другой endpoint
/usr/libexec/warp-manager disable
/usr/libexec/warp-manager enable
```

Последняя автоматическая попытка восстановления и доступная в тот момент память
записываются в `/etc/warp/last_recovery`. После неожиданной перезагрузки:

```sh
cat /etc/warp/last_recovery 2>/dev/null
free -m
logread | grep -Ei 'warp|oom|out of memory|killed process|watchdog|panic|thermal'
dmesg | grep -Ei 'oom|out of memory|killed process|watchdog|panic|thermal'
```

## Удаление

```sh
/usr/libexec/warp-manager unregister
apk del luci-i18n-warp-ru luci-app-warp warp-awg
```

Если сохранился вспомогательный пакет старой версии:

```sh
apk del warp-warpscout
```

## Безопасность и совместимость

- ключ и токен хранятся в `/etc/warp/awg-account.json` с правами `0600`;
- AWG-конфиг хранится в `/etc/warp/awg.conf` с правами `0600`;
- временный сканер загружается только для регистрации и явного поиска endpoint;
- userspace-бэкенд не зависит от версии ядра и не требует `kmod-amneziawg`;
- Zapret, sing-box, Forkop, PBR, DNS и firewall автоматически не изменяются;
- без вышестоящего интерфейса приложение вообще не добавляет маршруты;
- IPv4 и IPv6 доступны внутри одного интерфейса, транспорт endpoint — IPv4/UDP.

## Сборка

Корневой каталог собирает LuCI-пакет. `warp-awg/` содержит OpenWrt-рецепт
userspace AmneziaWG и минимального UAPI-контроллера, `warp-warpscout/` — рецепт
специализированного временного помощника регистрации и проверки AWG endpoint.

```sh
./tests/run.sh
```

APK и `SHA256SUMS` находятся в
[последнем GitHub Release](https://github.com/BAzeRlok/CFWARP-OPENWRT/releases/latest).
Предыдущая реализация сохранена в ветке
[`archive/masque-v2.0.1`](https://github.com/BAzeRlok/CFWARP-OPENWRT/tree/archive/masque-v2.0.1).
