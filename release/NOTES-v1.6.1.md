## CFWARP for OpenWrt 25.12.x

Исправление стабильности MASQUE-туннеля:

- устранён принудительный обрыв HTTP/2 каждые 25 секунд: отключены несовместимые
  HTTP/2 PING-таймеры, TCP keepalive сохранён;
- QUIC снова выбран транспортом по умолчанию с keepalive 5 секунд;
- reconnect после потери туннеля ускорен с 1 секунды до 250 мс;
- единственный постоянный TUN reader сохраняется между reconnect, а исходящие
  пакеты на короткое время помещаются в очередь;
- обычный idle-разрыв рабочего QUIC больше не включает HTTP/2 fallback;
- режим `auto` включает HTTP/2 только после трёх последовательных ошибок
  установления QUIC;
- DNS, маршруты, firewall и настройки сторонних приложений не изменяются.

Установщик выбирает QUIC автоматически. После ручного обновления через `apk`:

```sh
uci set warp.main.masque_transport='quic'
uci set warp.main.keepalive='5'
uci commit warp
/usr/libexec/warp-manager reconnect
```
