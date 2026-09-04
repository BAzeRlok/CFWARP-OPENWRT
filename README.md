# WARP for OpenWrt

LuCI-приложение регистрирует Cloudflare WARP, находит рабочий зарубежный
endpoint и создаёт отдельный интерфейс `warp` через AmneziaWG. Пакет не меняет
маршруты, DNS и firewall: трафик пойдёт через WARP только после выбора этого
интерфейса в Forkop, PBR или другой системе маршрутизации.

## Установка

Поддерживаются `aarch64` и `aarch64_cortex-a53` на OpenWrt с `apk`:

```sh
wget -qO- https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh | sh
```

Имя в маскирующем первом пакете и исключаемые страны можно задать при установке:

```sh
wget -qO- https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh |
WARP_SNI=ozon.ru WARP_EXCLUDE_COUNTRIES=RU,BY sh
```

Установщик скачивает релиз `v3.0.0`, проверяет SHA-256, устанавливает автономный
userspace-бэкенд AmneziaWG и WARPSCOUT, затем регистрирует WARP, проверяет
endpoint реальным трафиком и запускает интерфейс. При первой настройке сначала
проверяются основные диапазоны WARP двумя I/O-потоками; полный проход по всем
диапазонам выполняется только если быстрый не нашёл подходящий зарубежный
endpoint. Качество выхода оценивается до внешнего адреса `8.8.8.8`, чтобы выбор
не ограничивался внутренним маршрутом Cloudflare до `1.1.1.1`. При повторной
установке уже проверенный endpoint переиспользуется.

## Использование

Откройте LuCI → Сеть → WARP. Кнопка «Переподключить» заново ищет
рабочий endpoint. По умолчанию исключены выходы в России и узел DME.

Основные настройки:

- имя интерфейса — `warp`;
- MTU — `1280`;
- persistent keepalive — `25` секунд;
- маскирующее имя — используется только в первом AWG-пакете;
- исключаемые страны — список ISO-кодов через запятую.

Проверка состояния:

```sh
/usr/libexec/warp-manager status
ip -s link show dev warp
logread -e warp-awg -e warp-watchdog -e warp
```

Проверка выхода без изменения основного маршрута:

```sh
curl -4 --interface warp --connect-timeout 10 --max-time 20 \
    https://1.1.1.1/cdn-cgi/trace |
    grep -E '^(ip|colo|warp|gateway)='
```

Ожидаемое значение: `warp=on`. После пяти неудачных проверок watchdog мягко
перезапускает туннель с текущим endpoint. Новый endpoint ищется только по кнопке
«Переподключить», чтобы фоновое восстановление не создавало скачков нагрузки.

Последняя автоматическая попытка восстановления и доступная в тот момент память
записываются в `/etc/warp/last_recovery`. Если роутер неожиданно перезагрузился,
проверьте:

```sh
cat /etc/warp/last_recovery 2>/dev/null
free -m
logread | grep -Ei 'warp|oom|out of memory|killed process|watchdog|panic|thermal'
dmesg | grep -Ei 'oom|out of memory|killed process|watchdog|panic|thermal'
```

`logread` обычно хранится в RAM и очищается при перезагрузке. Для точного разбора
повторной перезагрузки заранее включите удалённый системный журнал OpenWrt.

## Удаление

Сначала удалите регистрацию Cloudflare и созданный сетевой интерфейс:

```sh
/usr/libexec/warp-manager unregister
```

Затем удалите пакеты:

```sh
apk del luci-i18n-warp-ru luci-app-warp warp-awg warp-warpscout
```

## Безопасность и совместимость

- ключ и токен хранятся в `/etc/warp/awg-account.json` с правами `0600`;
- AWG-конфиг хранится в `/etc/warp/awg.conf` с правами `0600`;
- endpoint выбирается по реальной передаче данных внутри тестового туннеля;
- userspace-бэкенд не зависит от версии ядра и не требует `kmod-amneziawg`;
- Zapret, sing-box, Forkop, PBR, DNS и firewall автоматически не изменяются;
- IPv4 и IPv6 доступны внутри одного интерфейса, транспорт endpoint — IPv4/UDP.

## Сборка

Корневой каталог собирает LuCI-пакет. `warp-awg/` содержит OpenWrt-рецепт
userspace AmneziaWG и минимального UAPI-контроллера, `warp-warpscout/` — рецепт
регистратора и сканера endpoint. Статические проверки:

```sh
./tests/run.sh
```

APK и `SHA256SUMS` находятся в
[последнем GitHub Release](https://github.com/BAzeRlok/CFWARP-OPENWRT/releases/latest).
Предыдущая реализация сохранена в ветке
[`archive/masque-v2.0.1`](https://github.com/BAzeRlok/CFWARP-OPENWRT/tree/archive/masque-v2.0.1).
