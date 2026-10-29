# WARP v4.1.1

Автономный WARP-интерфейс для OpenWrt 25.12 и full-tunnel клиент для Windows.

- Windows-клиент теперь доступен как единый нативный EXE: достаточно скачать
  файл и запустить его двойным щелчком.
- EXE сам запрашивает права администратора, предлагает принять условия
  Cloudflare, устанавливает проверенный официальный AmneziaWG, регистрирует
  WARP и выбирает endpoint с минимальной потерей пакетов и задержкой.
- Для обычной Windows x64 используется `warp-windows.exe`, для Windows on ARM —
  `warp-windows-arm64.exe`.
- По умолчанию Windows работает как full-tunnel: IPv4, IPv6 и DNS направляются
  через WARP.
- Сохранены команды `Status`, `Connect`, `Disconnect`, `Reconnect`, `Rescan` и
  `Uninstall` для управления из PowerShell или командной строки.
- На OpenWrt watchdog измеряет среднюю задержку и после трёх последовательных
  превышений порога сравнивает текущий endpoint со свежими кандидатами.
- OpenWrt не изменяет DNS, firewall и маршрутизацию пользовательского трафика
  автоматически.

OpenWrt:

```sh
wget -qO- https://raw.githubusercontent.com/BAzeRlok/CFWARP-OPENWRT/main/install.sh | sh
```

Windows: скачайте подходящий EXE из списка файлов релиза и запустите его.
