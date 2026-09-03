# Проверки

`./tests/run.sh` выполняется на машине сборки. Он проверяет shell, JSON, PO,
ACL/RPC allowlist, отсутствие автоматических маршрутов и firewall-правил,
транзакционный откат, AWG UAPI-контроллер и зафиксированные upstream-исходники.

`tests/router-integration.sh` предназначен для одноразового тестового роутера с
OpenWrt 25.12. Он выполняет реальную регистрацию и поэтому требует явного
`WARP_ACCEPT_CLOUDFLARE_TERMS=YES`. Скрипт сравнивает IPv4/IPv6-маршруты,
`firewall`, `dhcp` и resolv-файл до/после, проверяет userspace AWG TUN,
реальный `warp=on`, права файлов, конфликт имён, отключение, переподключение,
удаление и отсутствие секретов в ubus/logread.

Проверка перезагрузки выполняется двумя фазами:

```sh
WARP_ACCEPT_CLOUDFLARE_TERMS=YES ./router-integration.sh pre-reboot
reboot
WARP_ACCEPT_CLOUDFLARE_TERMS=YES ./router-integration.sh post-reboot
```

Сценарии отсутствия WAN следует выполнять только на изолированном стенде.
