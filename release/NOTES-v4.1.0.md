# WARP v4.1.0

Автономный WARP-интерфейс для OpenWrt 25.12 и full-tunnel клиент для Windows.

- Watchdog измеряет среднюю задержку внутри туннеля и реагирует только после
  трёх последовательных превышений заданного порога.
- При высокой задержке текущий endpoint сравнивается со свежими кандидатами по
  потере пакетов и ping; если он остаётся лучшим, туннель не перезапускается.
- Автоматический поиск ограничен одним запуском в 30 минут и использует
  настраиваемое число кандидатов, чтобы не перегружать роутер.
- Текущая задержка показывается в LuCI, результат последнего сравнения хранится
  в `/etc/warp/last_optimization`.
- Добавлен клиент Windows x64/ARM64: отдельная регистрация, выбор endpoint и
  full-tunnel для IPv4, IPv6 и DNS через официальную службу AmneziaWG/Wintun.
- Windows helper и официальный MSI проверяются по SHA-256; дополнительно
  проверяется Authenticode-подпись MSI. Ключи закрыты ACL для `SYSTEM` и
  администраторов.
- OpenWrt по-прежнему не изменяет DNS, firewall и маршрутизацию пользовательского
  трафика автоматически.

OpenWrt:

```sh
wget -qO- https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh | sh
```

Windows PowerShell от имени администратора:

```powershell
Set-ExecutionPolicy -Scope Process Bypass
Invoke-WebRequest `
  https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/windows/warp.ps1 `
  -OutFile "$env:TEMP\warp.ps1"
& "$env:TEMP\warp.ps1" -Action Install -AcceptCloudflareTerms
```
