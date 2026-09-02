# CFWARP v1.7.3

- HTTP/2 plain CONNECT теперь точно повторяет приватный wire format Cloudflare:
  используется `cf-connect-proto: cf-connect-ip` без объявления несовместимого
  RFC-расширения `capsule-protocol`.
- После фрагментации TLS ClientHello сохраняется `TCP_NODELAY`; малые
  CONNECT-IP DATA frames больше не задерживаются алгоритмом Nagle.
- Добавлен регрессионный Go-тест точного набора HTTP/2 CONNECT-заголовков.
- Сохранены исправление низкой задержки QUIC и очередь в один пакет из v1.7.2.

Обновлены пакеты:

- `luci-app-warp` 1.7.3-r1;
- `warp-usque` 4.2.1-r6.
