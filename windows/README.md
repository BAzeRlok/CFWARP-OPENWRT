# WARP для Windows

Клиент создаёт отдельную регистрацию Cloudflare WARP, ищет AWG endpoint с
минимальной потерей пакетов и задержкой и устанавливает системную службу
`AmneziaWGTunnel$warp`. По умолчанию это full-tunnel: маршруты `0.0.0.0/0` и
`::/0`, а также указанные DNS-серверы направляются через WARP.

Сетевой драйвер и служба не реализуются проектом заново. Клиент устанавливает
[официальный AmneziaWG for Windows](https://github.com/amnezia-vpn/amneziawg-windows-client),
проверяя SHA-256 и Authenticode-подпись MSI. Регистрация и поиск endpoint
выполняются helper, чья SHA-256 зафиксирована в клиенте. Ключи хранятся в
`%ProgramData%\WARP` с доступом только для `SYSTEM` и администраторов.

## Установка

Скачайте из [последнего релиза](https://github.com/BAzeRlok/CFWARP-OPENWRT/releases/latest):

- `warp-windows.exe` — обычная Windows x64;
- `warp-windows-arm64.exe` — Windows on ARM.

Запустите EXE двойным щелчком. Клиент сам запросит права администратора,
покажет ссылки на условия Cloudflare, установит проверенные компоненты и
подключит WARP. Пользователю нужен только один скачанный EXE; официальный MSI и
вспомогательный компонент загружаются и проверяются автоматически.

Ответ в окне подтверждает, что пользователь прочитал
[условия Cloudflare](https://www.cloudflare.com/application/terms/) и
[политику конфиденциальности](https://www.cloudflare.com/privacypolicy/).

По умолчанию исключаются российские входные узлы Cloudflare. Пример более
глубокого поиска с ограничением по узлам:

```powershell
.\warp-windows.exe Rescan --sample 8 `
  --required-countries 'NL,DE,FI,SE' --required-nodes 'AMS,FRA,HEL,ARN'
```

Код входного узла не гарантирует страну публичного WARP-выхода: Cloudflare
использует anycast.

## Управление

```powershell
.\warp-windows.exe Status
.\warp-windows.exe Disconnect
.\warp-windows.exe Connect
.\warp-windows.exe Reconnect
.\warp-windows.exe Rescan --sample 5
.\warp-windows.exe Uninstall
```

Для автоматической установки без интерактивного подтверждения:

```powershell
.\warp-windows.exe Install --accept-cloudflare-terms
```

`Uninstall` удаляет службу WARP, конфигурацию и локальные ключи, но оставляет
официальное приложение AmneziaWG установленным.
