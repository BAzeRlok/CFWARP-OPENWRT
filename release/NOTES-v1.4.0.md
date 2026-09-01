## CFWARP for OpenWrt 25.12.x

Первый публичный релиз автономного LuCI-приложения Cloudflare WARP.

- отдельный автономный MASQUE backend `warp-usque`;
- Chrome-подобный TLS ClientHello с фрагментацией для регистрации Cloudflare;
- QUIC/HTTP3 и TCP/HTTP2;
- TUN-интерфейс без автоматического default route, firewall, NAT или DNS;
- исправлена обработка ALPN HTTP/2 при регистрации;
- менеджер принимает полностью сохранённый профиль даже при задержке завершения backend;
- установка одной командой с обязательной проверкой SHA-256.

Готовый `warp-usque` в этом релизе предназначен для архитектуры
`aarch64_cortex-a53` (включая использованный для проверки mediatek/filogic).
