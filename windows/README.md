# WARP для Windows

Клиент создаёт отдельную регистрацию Cloudflare WARP, ищет AWG endpoint с
минимальной потерей пакетов и задержкой и устанавливает системную службу
`AmneziaWGTunnel$warp`. По умолчанию это full-tunnel: маршруты `0.0.0.0/0` и
`::/0`, а также указанные DNS-серверы направляются через WARP.

Сетевой драйвер и служба не реализуются проектом заново. Скрипт устанавливает
[официальный AmneziaWG for Windows](https://github.com/amnezia-vpn/amneziawg-windows-client),
проверяя SHA-256 и Authenticode-подпись MSI. Регистрация и поиск endpoint
выполняются нашим подписанным контрольной суммой helper. Ключи хранятся в
`%ProgramData%\WARP` с доступом только для `SYSTEM` и администраторов.

## Установка

Откройте PowerShell 5.1 или новее **от имени администратора**:

```powershell
Set-ExecutionPolicy -Scope Process Bypass
Invoke-WebRequest `
  https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/windows/warp.ps1 `
  -OutFile "$env:TEMP\warp.ps1"
& "$env:TEMP\warp.ps1" -Action Install -AcceptCloudflareTerms
```

Флаг `-AcceptCloudflareTerms` подтверждает, что пользователь прочитал
[условия Cloudflare](https://www.cloudflare.com/application/terms/) и
[политику конфиденциальности](https://www.cloudflare.com/privacypolicy/).

По умолчанию исключаются российские входные узлы Cloudflare. Пример более
глубокого поиска с ограничением по узлам:

```powershell
& "$env:TEMP\warp.ps1" -Action Rescan -Sample 8 `
  -RequiredCountries 'NL,DE,FI,SE' -RequiredNodes 'AMS,FRA,HEL,ARN'
```

Код входного узла не гарантирует страну публичного WARP-выхода: Cloudflare
использует anycast.

## Управление

```powershell
& "$env:TEMP\warp.ps1" -Action Status
& "$env:TEMP\warp.ps1" -Action Disconnect
& "$env:TEMP\warp.ps1" -Action Connect
& "$env:TEMP\warp.ps1" -Action Reconnect
& "$env:TEMP\warp.ps1" -Action Rescan -Sample 5
& "$env:TEMP\warp.ps1" -Action Uninstall
```

`Uninstall` удаляет службу WARP, конфигурацию и локальные ключи, но оставляет
официальное приложение AmneziaWG установленным.
