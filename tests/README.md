# Проверки

`./tests/run.sh` выполняется на машине сборки. Он проверяет синтаксис shell,
JSON и PO, ACL/RPC allowlist, защитные UCI-паметры, автономность backend
и безопасную обработку профиля и API-токена.

`tests/router-integration.sh` предназначен для одноразового тестового роутера с
OpenWrt 25.12.5. Он выполняет реальную регистрацию и поэтому требует явного
`WARP_ACCEPT_CLOUDFLARE_TERMS=YES`. Скрипт сравнивает IPv4/IPv6-маршруты,
`firewall`, `dhcp` и resolv-файл до/после, проверяет userspace TUN, права
профиля usque, идемпотентность, конфликт имён, отключение,
переподключение, удаление и отсутствие секретов в ubus/logread.

Проверка перезагрузки выполняется двумя фазами:

```sh
WARP_ACCEPT_CLOUDFLARE_TERMS=YES ./router-integration.sh pre-reboot
reboot
WARP_ACCEPT_CLOUDFLARE_TERMS=YES ./router-integration.sh post-reboot
```

Сценарии отсутствия WAN и полностью отключённого IPv6 следует выполнять на
изолированном стенде. Основная проверка использует автономный `usque_masque` и
отдельный системный TUN без автоматических маршрутов.
