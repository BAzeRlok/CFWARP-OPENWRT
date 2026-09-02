## CFWARP for OpenWrt 25.12.x

Обновление стабильности MASQUE-туннеля:

- `warp-usque` обновлён до upstream `usque` 4.2.1;
- добавлен актуальный upstream fix длины QUIC Connection ID для Cloudflare;
- стабильный TCP/HTTP2 выбран транспортом по умолчанию;
- HTTP/2 соединение поддерживается HTTP/2 PING и TCP keepalive;
- добавлен адаптивный режим: QUIC → постоянный HTTP/2 после первого сбоя;
- QUIC keepalive по умолчанию уменьшен до 5 секунд;
- сохранены локальные anti-DPI-модификации регистрации и TCP/HTTP2;
- DNS, маршруты, firewall и настройки сторонних приложений не изменяются.

Для существующей установки, обновлённой вручную через `apk`, выберите новый
стабильный режим явно:

```sh
uci set warp.main.masque_transport='http2'
uci commit warp
/usr/libexec/warp-manager reconnect
```
