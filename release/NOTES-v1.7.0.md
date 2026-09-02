## CFWARP for OpenWrt 25.12.x

Исправление HTTP/2 CONNECT-IP data plane:

- высокоуровневый `http.Client` заменён на ручной HTTP/2 framing, совместимый
  с фактическим протоколом Cloudflare WARP;
- отправляется plain CONNECT с `cf-connect-proto: cf-connect-ip` и
  `capsule-protocol: ?1`, без несовместимых pseudo-headers;
- IP-пакеты передаются в Capsule Protocol DATAGRAM внутри HTTP/2 DATA frames;
- реализованы SETTINGS, двусторонний flow control, PING ACK, GOAWAY и
  RST_STREAM с корректным reconnect вместо молчащего туннеля;
- HTTP/2 всегда использует канонический SNI
  `consumer-masque.cloudflareclient.com`; маскирующий пользовательский SNI
  продолжает применяться к QUIC/HTTP3;
- сохранены pinning публичного ключа endpoint, TLS ClientHello fragmentation,
  быстрый reconnect и постоянный TUN reader;
- добавлены тесты wire-format заголовков, Capsule framing, выбора SNI и
  IPv4 TTL/checksum.

Захват `tcpdump` подтвердил исходную неисправность: корректные IP-пакеты уходили
в TUN, но после формально успешного ответа HTTP `200` backend не получал ни
одного обратного IP-пакета. Маршруты, firewall, MTU и `zapret` причиной не были.
