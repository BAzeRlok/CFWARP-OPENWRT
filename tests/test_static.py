#!/usr/bin/python3

import json
import pathlib
import re
import subprocess
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
MANAGER = ROOT / "root/usr/libexec/warp-manager"
API = ROOT / "root/usr/libexec/warp-api"
RPC = ROOT / "root/usr/share/rpcd/ucode/warp.uc"
FRONTEND = ROOT / "htdocs/luci-static/resources/view/warp/overview.js"


def catalog_msgids(path):
    messages = set()
    current = None
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.startswith("msgid "):
            if current is not None:
                messages.add(current)
            current = json.loads(line[6:])
        elif current is not None and line.startswith('"'):
            current += json.loads(line)
        elif current is not None:
            messages.add(current)
            current = None
    if current is not None:
        messages.add(current)
    return messages


class PackageTests(unittest.TestCase):
    def test_shell_syntax(self):
        for path in [
            MANAGER,
            API,
            ROOT / "root/etc/init.d/warp",
            ROOT / "install.sh",
            ROOT / "tests/router-integration.sh",
            ROOT / "tests/run.sh",
        ]:
            subprocess.run(["sh", "-n", str(path)], check=True)

    def test_json_files(self):
        for path in ROOT.rglob("*.json"):
            json.loads(path.read_text(encoding="utf-8"))

    def test_manager_action_allowlist_is_exact(self):
        text = MANAGER.read_text(encoding="utf-8")
        dispatch = re.search(r'case "\$\{1:-\}" in\n(?P<body>.*?)\nesac\s*$', text, re.S)
        self.assertIsNotNone(dispatch)
        actions = re.findall(r'^\s*([a-z]+)\)\s+do_', dispatch.group("body"), re.M)
        self.assertEqual(actions, ["register", "enable", "disable", "reconnect", "status", "unregister"])

    def test_route_and_dns_safety_options(self):
        text = MANAGER.read_text(encoding="utf-8")
        required = [
            "defaultroute='0'",
            "peerdns='0'",
            "proto='none'",
            "warp_backend='usque_masque'",
        ]
        for value in required:
            self.assertIn(value, text)
        self.assertNotRegex(text, r"\b(ip|route)\s+(route|rule)\s+(add|replace|del)")
        self.assertNotIn("/etc/config/firewall", text)
        self.assertNotIn("uci-firewall", text)
        self.assertNotRegex(text, r"set (dhcp|firewall)\.")

    def test_transactional_section_only_rollback(self):
        text = MANAGER.read_text(encoding="utf-8")
        self.assertIn("export network", text)
        self.assertIn("-m import network", text)
        self.assertIn("rollback_configuration", text)
        self.assertIn("runtime_restart_service", text)
        self.assertNotIn('show "network.$ACTUAL_INTERFACE" >>"$temp_snapshot"', text)

    def test_legacy_backends_are_absent(self):
        manager = MANAGER.read_text(encoding="utf-8")
        init = (ROOT / "root/etc/init.d/warp").read_text(encoding="utf-8")
        frontend = FRONTEND.read_text(encoding="utf-8")
        for legacy in [
            "wireguard_reserved",
            "forkop_masque",
            "forkop_warp",
            "sing-box",
            "actual_peer",
            "masque-cache.db",
            "registration.json",
            "private.key",
        ]:
            self.assertNotIn(legacy, manager + init + frontend)
        self.assertIn('procd_open_instance warp', init)

    def test_standalone_usque_backend_is_isolated_and_patched(self):
        manager = MANAGER.read_text(encoding="utf-8")
        init = (ROOT / "root/etc/init.d/warp").read_text(encoding="utf-8")
        package = (ROOT / "warp-usque/Makefile").read_text(encoding="utf-8")
        patch = (ROOT / "warp-usque/patches/100-antidpi-cloudflare-api.patch").read_text(encoding="utf-8")
        self.assertIn('USQUE_BIN="${WARP_USQUE_BIN:-/usr/libexec/warp-usque}"', manager)
        self.assertIn("set warp.main.actual_backend='usque_masque'", manager)
        self.assertIn('usque_profile_valid "$temp_usque_config"', manager)
        self.assertIn('kill -TERM "$temp_usque_pid"', manager)
        self.assertIn("/usr/libexec/warp-usque", init)
        self.assertIn("nativetun", init)
        self.assertNotIn("/etc/config/zapret", manager + init)
        self.assertNotIn("/opt/zapret", manager + init)
        self.assertIn("PKG_HASH:=b8c77254d8b909e99b7b58d1bbbb4222ba436a1dad0967710406915be2481ef5", package)
        self.assertIn("HelloChrome_Auto", patch)
        self.assertIn("forceHTTP1ALPN", patch)
        self.assertIn('AlpnProtocols: []string{"http/1.1"}', patch)
        self.assertIn("clientHelloFragmentConn", patch)
        self.assertIn("ServerName: host", patch)
        self.assertNotIn("--insecure", init)

    def test_runtime_start_reports_the_failed_phase(self):
        manager = MANAGER.read_text(encoding="utf-8")
        for error_code in [
            "backend_start_failed",
            "tunnel_start_timeout",
            "network_reload_failed",
            "interface_up_failed",
        ]:
            self.assertIn(f"RUNTIME_START_ERROR={error_code}", manager)
        self.assertEqual(manager.count('emit_error "$RUNTIME_START_ERROR"'), 1)

    def test_rpc_has_no_command_parameter(self):
        text = RPC.read_text(encoding="utf-8")
        self.assertNotIn("request.args.command", text)
        self.assertNotIn("request.args.action", text)
        self.assertIn("request.args.accept_terms !== true", text)
        self.assertIn("const allowed", text)

    def test_acl_is_minimal(self):
        acl = json.loads((ROOT / "root/usr/share/rpcd/acl.d/luci-app-warp.json").read_text())
        grant = acl["luci-app-warp"]
        self.assertEqual(grant["read"]["uci"], ["warp"])
        self.assertEqual(grant["write"]["uci"], ["warp"])
        self.assertEqual(grant["read"]["ubus"]["luci.warp"], ["status"])
        self.assertNotIn("file", grant["write"])

    def test_frontend_does_not_request_secrets(self):
        text = FRONTEND.read_text(encoding="utf-8")
        for secret in ["private_key", "token", "registration.json", "client_id"]:
            self.assertNotIn(secret, text)

    def test_translations_compile(self):
        with tempfile.NamedTemporaryFile() as output:
            subprocess.run(["msgfmt", "-c", "-o", output.name, str(ROOT / "po/ru/warp.po")], check=True)

    def test_frontend_messages_are_in_both_catalogs(self):
        frontend = FRONTEND.read_text(encoding="utf-8")
        messages = set(re.findall(r"_\('([^']+)'\)", frontend))
        for catalog_path in [ROOT / "po/templates/warp.pot", ROOT / "po/ru/warp.po"]:
            catalog = catalog_msgids(catalog_path)
            for message in messages:
                self.assertIn(message, catalog, f"{message!r} missing from {catalog_path}")

    def test_package_is_architecture_independent(self):
        makefile = (ROOT / "Makefile").read_text(encoding="utf-8")
        self.assertIn("LUCI_PKGARCH:=all", makefile)
        self.assertIn("LUCI_NAME:=luci-app-warp", makefile)
        self.assertIn("LUCI_EXTRA_DEPENDS:=kmod-tun (>=0), warp-usque (>=2.0.1)", makefile)
        self.assertNotIn("+kmod-tun", makefile)
        self.assertNotIn("+warp-usque", makefile)
        self.assertNotIn("+wireguard-tools", makefile)
        self.assertNotIn("+sing-box", makefile)
        self.assertNotIn("+kmod-wireguard", makefile)
        self.assertNotIn("+luci-proto-wireguard", makefile)
        self.assertNotIn("firewall4", makefile)
        self.assertNotIn("pbr", makefile)

    def test_installer_selects_supported_arm64_package(self):
        installer = (ROOT / "install.sh").read_text(encoding="utf-8")
        self.assertIn("aarch64|aarch64_cortex-a53)", installer)
        self.assertIn('USQUE_PACKAGE="warp-usque-2.0.1-r3-$ARCH.apk"', installer)
        self.assertIn('RELEASE_TAG="v1.4.1"', installer)


if __name__ == "__main__":
    unittest.main(verbosity=2)
