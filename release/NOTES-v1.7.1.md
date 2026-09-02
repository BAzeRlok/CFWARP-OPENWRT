## CFWARP for OpenWrt 25.12.x

Исправление подключения HTTP/2 после `v1.7.0`:

- Cloudflare MASQUE endpoint может завершить TLS без выбранного ALPN, хотя
  принимает HTTP/2 client preface и CONNECT-IP;
- пустой ALPN теперь разрешён как HTTP/2 prior knowledge, как это неявно делал
  использованный ранее `http2.Transport`;
- явный выбор другого протокола (`http/1.1`, `h3`) по-прежнему считается
  ошибкой и закрывает соединение;
- добавлен unit-тест обоих допустимых и недопустимых вариантов ALPN.

Исправляет цикл `HTTP/2 endpoint negotiated ALPN ""`, замеченный на реальном
OpenWrt-устройстве. Реализация raw CONNECT-IP из `v1.7.0` сохранена.
