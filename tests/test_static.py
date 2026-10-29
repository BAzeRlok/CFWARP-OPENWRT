#!/usr/bin/python3

import json
import pathlib
import re
import socket
import subprocess
import tempfile
import threading
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
MANAGER = ROOT / "root/usr/libexec/warp-manager"
API = ROOT / "root/usr/libexec/warp-api"
RUNNER = ROOT / "root/usr/libexec/warp-awg-runner"
WATCHDOG = ROOT / "root/usr/libexec/warp-watchdog"
RPC = ROOT / "root/usr/share/rpcd/ucode/warp.uc"
FRONTEND = ROOT / "htdocs/luci-static/resources/view/warp/overview.js"
AWG_CTL = ROOT / "warp-awg/files/warp-awgctl.c"


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
            RUNNER,
            WATCHDOG,
            ROOT / "root/etc/init.d/warp",
            ROOT / "root/etc/init.d/warp-watchdog",
            ROOT / "install.sh",
            ROOT / "tools/test-direct-regions.sh",
            ROOT / "tools/build-windows-client.sh",
            ROOT / "tests/router-integration.sh",
            ROOT / "tests/run.sh",
            ROOT / "tests/test_watchdog.sh",
        ]:
            subprocess.run(["sh", "-n", str(path)], check=True)

    def test_awg_controller_builds(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "warp-awgctl"
            subprocess.run(
                ["cc", "-std=c11", "-Os", "-Wall", "-Wextra", "-Werror", "-o", output, AWG_CTL],
                check=True,
            )
            result = subprocess.run([output], text=True, capture_output=True)
            self.assertEqual(result.returncode, 2)
            self.assertIn("setconf INTERFACE FILE", result.stderr)

    def test_awg_controller_uapi(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            output = root / "warp-awgctl"
            config = root / "awg.conf"
            socket_path = root / "warp.sock"
            subprocess.run(
                ["cc", "-std=c11", "-Os", "-Wall", "-Wextra", "-Werror", "-o", output, AWG_CTL],
                check=True,
            )
            config.write_text(
                """# MTU: 1280
[Interface]
PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
Jc = 6
Jmin = 10
Jmax = 50
I1 = <r 2><b 0x010203>

[Peer]
PublicKey = bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=
Endpoint = 162.159.192.1:2408
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
""",
                encoding="utf-8",
            )

            requests = []
            ready = threading.Event()

            def server():
                with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener:
                    listener.bind(str(socket_path))
                    listener.listen(2)
                    ready.set()
                    for response in [b"errno=0\n\n", b"rx_bytes=1234\nerrno=0\n\n"]:
                        connection, _ = listener.accept()
                        with connection:
                            request = b""
                            while b"\n\n" not in request:
                                request += connection.recv(4096)
                            requests.append(request.decode())
                            connection.sendall(response)

            worker = threading.Thread(target=server)
            worker.start()
            ready.wait(timeout=2)
            environment = {"WARP_AWG_SOCKET_DIR": directory}
            subprocess.run([output, "setconf", "warp", config], env=environment, check=True)
            result = subprocess.run(
                [output, "get", "warp", "rx_bytes"],
                env=environment,
                text=True,
                capture_output=True,
                check=True,
            )
            worker.join(timeout=2)
            self.assertFalse(worker.is_alive())
            self.assertEqual(result.stdout, "1234\n")
            self.assertIn("replace_peers=true\n", requests[0])
            self.assertIn("endpoint=162.159.192.1:2408\n", requests[0])
            self.assertEqual(requests[1], "get=1\n\n")

    def test_json_files(self):
        for path in ROOT.rglob("*.json"):
            if "logs" not in path.parts:
                json.loads(path.read_text(encoding="utf-8"))

    def test_manager_action_allowlist(self):
        text = MANAGER.read_text(encoding="utf-8")
        dispatch = re.search(r'case "\$\{1:-\}" in\n(?P<body>.*?)\nesac\s*$', text, re.S)
        self.assertIsNotNone(dispatch)
        actions = re.findall(r'^\s*([a-z]+)\)\s+do_', dispatch.group("body"), re.M)
        self.assertEqual(
            actions,
            ["register", "enable", "disable", "reconnect", "rescan", "status", "unregister", "recover", "optimize"],
        )
        rpc = RPC.read_text(encoding="utf-8")
        self.assertNotIn("recover: true", rpc)
        self.assertNotIn("optimize: true", rpc)

    def test_route_dns_and_firewall_safety(self):
        manager = MANAGER.read_text(encoding="utf-8")
        runner = RUNNER.read_text(encoding="utf-8")
        required = [
            "defaultroute='0'",
            "peerdns='0'",
            "proto='none'",
            "warp_backend='amneziawg'",
        ]
        for value in required:
            self.assertIn(value, manager)
        self.assertNotRegex(manager, r"\bip\s+(route|rule)\s+(add|replace|del)")
        self.assertNotRegex(runner, r"\bip\s+rule\s+(add|replace|del)")
        self.assertIn('route replace "$route_endpoint/32"', runner)
        self.assertIn('route del "$route_endpoint/32"', runner)
        self.assertIn("metric 42760", runner)
        self.assertNotIn("/etc/config/firewall", manager + runner)
        self.assertNotRegex(manager, r"set (dhcp|firewall)\.")

    def test_transactional_section_and_endpoint_rollback(self):
        text = MANAGER.read_text(encoding="utf-8")
        for marker in [
            "export network",
            "-m import network",
            "rollback_configuration",
            "rollback_live_config",
            "temp_old_config",
            "prepare_tunnel_config",
        ]:
            self.assertIn(marker, text)
        connect = re.search(r"connect_tunnel\(\) \{(?P<body>.*?)\n\}", text, re.S).group("body")
        self.assertLess(connect.index('prepare_tunnel_config "$rescan"'), connect.index("runtime_stop"))

    def test_awg_backend_is_standalone(self):
        manager = MANAGER.read_text(encoding="utf-8")
        init = (ROOT / "root/etc/init.d/warp").read_text(encoding="utf-8")
        runner = RUNNER.read_text(encoding="utf-8")
        watchdog = WATCHDOG.read_text(encoding="utf-8")
        awg_package = (ROOT / "warp-awg/Makefile").read_text(encoding="utf-8")
        awg_memory_patch = (ROOT / "warp-awg/patches/100-openwrt-memory.patch").read_text(encoding="utf-8")
        scout_package = (ROOT / "warp-warpscout/Makefile").read_text(encoding="utf-8")
        scout_patch = (ROOT / "warp-warpscout/patches/100-awg-only.patch").read_text(encoding="utf-8")
        scout_minimal_patch = (ROOT / "warp-warpscout/patches/110-minimal-helper.patch").read_text(encoding="utf-8")

        self.assertIn('SCOUT_BIN="${WARP_SCOUT_BIN:-}"', manager)
        self.assertIn("ensure_scout()", manager)
        self.assertIn("scout_integrity_failed", manager)
        self.assertIn("warp-warpscout-$SCOUT_VERSION-$architecture.apk", manager)
        self.assertIn("command -v apk", manager)
        self.assertIn("command -v sha256sum", manager)
        self.assertIn("command -v ip", manager)
        self.assertIn("Missing executable or file", manager)
        self.assertIn("--proto awg", manager)
        self.assertIn("--gen-i1 quic", manager)
        self.assertIn('--tunnel-jobs "$SCOUT_JOBS"', manager)
        self.assertIn('SCOUT_JOBS="${WARP_SCOUT_JOBS:-2}"', manager)
        self.assertIn("SCOUT_FAST_TARGETS='162.159.192.0/24,188.114.96.0/24,188.114.97.0/24'", manager)
        self.assertIn("SCOUT_PING_TARGET='8.8.8.8'", manager)
        self.assertIn('if [ "$CFG_SCAN_SAMPLE" -gt 1 ]', manager)
        self.assertNotIn("INITIAL_SCAN", manager)
        self.assertIn('run_endpoint_scan 1 "$SCOUT_FAST_TARGETS"', manager)
        self.assertIn('run_endpoint_scan "$CFG_SCAN_SAMPLE"', manager)
        self.assertIn('GOMAXPROCS=1 NO_COLOR=1 "$SCOUT_BIN"', manager)
        self.assertIn("--tun-ping-count 10", manager)
        self.assertIn('--ping-target "$SCOUT_PING_TARGET"', manager)
        self.assertIn("--exclude-country", manager)
        self.assertIn('--country "$CFG_NODE_COUNTRIES"', manager)
        self.assertIn('--node "$CFG_NODES"', manager)
        self.assertIn('--interface "$UPSTREAM_DEVICE"', manager)
        self.assertIn("connect_tunnel 'reconnected' 0 0", manager)
        self.assertIn("connect_tunnel 'rescanned' 1 0", manager)
        self.assertIn("set warp.main.actual_backend='amneziawg'", manager)
        self.assertIn("/usr/libexec/warp-awg-runner", init)
        self.assertIn("/usr/libexec/warp-amneziawg-go", runner)
        self.assertIn("/usr/libexec/warp-awgctl", runner)
        self.assertIn('address replace "$ipv4/32"', runner)
        self.assertIn("install_upstream_route", runner)
        self.assertIn("restarting the current endpoint", watchdog)
        self.assertIn("$MANAGER recover", watchdog)
        self.assertIn('MAX_FAILURES="${WARP_WATCHDOG_FAILURES:-5}"', watchdog)
        self.assertIn('LATENCY_FAILURES="${WARP_WATCHDOG_LATENCY_FAILURES:-3}"', watchdog)
        self.assertIn('$MANAGER optimize "$latency_ms"', watchdog)
        self.assertIn('measure_latency "$iface"', watchdog)
        self.assertIn('Latency optimization: current $OPTIMIZE_ENDPOINT', manager)
        self.assertIn('$SCOUT_FAST_TARGETS,$current_ip/32', manager)
        self.assertIn("'endpoint_kept'", manager)
        self.assertIn("last_rx=0", watchdog)
        self.assertNotIn('failures=$((failures + 1))\n\t\tcontinue', watchdog)
        self.assertNotIn("/etc/config/zapret", manager + init + runner)
        self.assertNotIn("/opt/zapret", manager + init + runner)

        self.assertIn("PKG_VERSION:=3.1.20260828", awg_package)
        self.assertIn("PKG_HASH:=24c656cfb80ff6855702710eaf2e3729fa710bf6bfdbbbdfba01984ccd17de95", awg_package)
        self.assertIn("warp-awgctl.c", awg_package)
        batch_size = re.search(r"^\+\s*IdealBatchSize\s*=\s*(\d+)", awg_memory_patch, re.M)
        self.assertIsNotNone(batch_size)
        # The Linux UDP GRO path reserves one slot per group of 64 datagrams.
        # A smaller batch produces an empty ReadBatch slice and panics.
        self.assertGreaterEqual(int(batch_size.group(1)), 64)
        self.assertIn("GOMEMLIMIT=48MiB", init)
        self.assertIn("WG_PROCESS_FOREGROUND=1", init)
        self.assertIn("PKG_VERSION:=0.16.0", scout_package)
        self.assertIn("PKG_RELEASE:=2", scout_package)
        self.assertIn("PKG_HASH:=c21c777239856401f6529e4e2503d9f6ebd9071f78988fe86e35aaefc65a1c20", scout_package)
        self.assertIn("return mintWGAccount(ctx, client, existing)", scout_patch)
        self.assertIn("//go:build warpscout_full", scout_patch)
        self.assertIn("diff --git a/mini_flags.go b/mini_flags.go", scout_minimal_patch)
        self.assertIn("warp-scanner <scan|register>", scout_minimal_patch)
        self.assertIn("//go:build warpscout_full", scout_minimal_patch)
        self.assertNotIn('github.com/Diniboy1123/connect-ip-go', scout_package)
        self.assertFalse((ROOT / "warp-usque/Makefile").exists())

    def test_runtime_contains_no_retired_transport(self):
        paths = [
            MANAGER,
            API,
            RUNNER,
            WATCHDOG,
            ROOT / "root/etc/init.d/warp",
            ROOT / "root/etc/config/warp",
            FRONTEND,
            ROOT / "install.sh",
        ]
        text = "\n".join(path.read_text(encoding="utf-8") for path in paths)
        self.assertNotRegex(text, re.compile(r"http.?2|masque|usque", re.I))

    def test_awg_controller_does_not_expose_keys(self):
        text = AWG_CTL.read_text(encoding="utf-8")
        self.assertIn('!strcmp(field, "last_handshake_time_sec")', text)
        self.assertIn('!strcmp(field, "rx_bytes")', text)
        self.assertNotIn('!strcmp(field, "private_key")', text)
        self.assertNotIn('!strcmp(field, "public_key")', text)
        self.assertIn("key must be 32-byte base64", text)
        self.assertIn("endpoint must contain a numeric IPv4 address and port", text)

    def test_runtime_start_reports_the_failed_phase(self):
        manager = MANAGER.read_text(encoding="utf-8")
        for error_code in [
            "backend_start_failed",
            "tunnel_start_timeout",
            "network_reload_failed",
            "interface_up_failed",
            "data_plane_unavailable",
        ]:
            self.assertIn(f"RUNTIME_START_ERROR={error_code}", manager)
        self.assertEqual(manager.count('emit_error "$RUNTIME_START_ERROR"'), 2)

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
        for secret in ["private_key", "token", "awg-account.json", "client_id"]:
            self.assertNotIn(secret, text)

    def test_translations_compile(self):
        with tempfile.NamedTemporaryFile() as output:
            subprocess.run(["msgfmt", "-c", "-o", output.name, ROOT / "po/ru/warp.po"], check=True)

    def test_frontend_messages_are_in_both_catalogs(self):
        frontend = FRONTEND.read_text(encoding="utf-8")
        messages = set(re.findall(r"_\('([^']+)'\)", frontend))
        for catalog_path in [ROOT / "po/templates/warp.pot", ROOT / "po/ru/warp.po"]:
            catalog = catalog_msgids(catalog_path)
            for message in messages:
                self.assertIn(message, catalog, f"{message!r} missing from {catalog_path}")

    def test_package_dependencies(self):
        makefile = (ROOT / "Makefile").read_text(encoding="utf-8")
        self.assertIn("LUCI_PKGARCH:=all", makefile)
        self.assertIn("LUCI_NAME:=luci-app-warp", makefile)
        self.assertIn("warp-awg (>=3.1.20260828-r3)", makefile)
        self.assertNotIn("warp-warpscout (>=", makefile)
        self.assertIn("LUCI_DEPENDS:=+luci-base +curl +jsonfilter", makefile)
        for unwanted in ["+wireguard-tools", "+sing-box", "+kmod-wireguard", "+luci-proto-wireguard", "firewall4", "pbr"]:
            self.assertNotIn(unwanted, makefile)

    def test_installer_selects_supported_arm64_packages(self):
        installer = (ROOT / "install.sh").read_text(encoding="utf-8")
        self.assertIn("aarch64|aarch64_cortex-a53)", installer)
        self.assertIn('LUCI_PACKAGE="luci-app-warp-4.2.0-r1.apk"', installer)
        self.assertIn('AWG_PACKAGE="warp-awg-3.1.20260828-r3-$ARCH.apk"', installer)
        self.assertNotIn('SCOUT_PACKAGE=', installer)
        self.assertIn("apk del warp-warpscout", installer)
        self.assertIn('result=$(/usr/libexec/warp-manager enable)', installer)
        self.assertIn("DISTRIB_ARCH", installer)
        self.assertIn("OPENWRT_ARCH", installer)
        self.assertIn('RELEASE_TAG="v4.2.0"', installer)
        self.assertIn('uci set warp.main.sni="$MASKING_SNI"', installer)

    def test_latency_watchdog_defaults_and_windows_full_tunnel(self):
        config = (ROOT / "root/etc/config/warp").read_text(encoding="utf-8")
        frontend = FRONTEND.read_text(encoding="utf-8")
        windows = (ROOT / "windows/warp.ps1").read_text(encoding="utf-8")
        launcher = (ROOT / "windows/main_windows.go").read_text(encoding="utf-8")
        for setting in [
            "option latency_watchdog '1'",
            "option latency_threshold '250'",
            "option latency_cooldown '1800'",
        ]:
            self.assertIn(setting, config)
        self.assertIn("'latency_watchdog'", frontend)
        self.assertIn("'latency_threshold'", frontend)
        self.assertIn("'scan_sample'", frontend)
        self.assertIn("0\\.0\\.0\\.0/0,\\s*::/0", windows)
        self.assertIn("AmneziaWGTunnel$warp", windows)
        self.assertIn("Get-AuthenticodeSignature", windows)
        self.assertIn("'*S-1-5-18:F'", windows)
        self.assertNotIn("__HELPER_", windows)
        self.assertIn("//go:embed warp.ps1", launcher)
        self.assertIn('NewProc("ShellExecuteW")', launcher)
        self.assertIn('NewProc("CreateWindowExW")', launcher)
        self.assertIn('createButton("Лучший endpoint"', launcher)
        self.assertIn('createButton("Подключить"', launcher)
        self.assertIn('createButton("Отключить"', launcher)
        self.assertIn("[Console]::OutputEncoding", windows)
        build_script = (ROOT / "tools/build-windows-client.sh").read_text(encoding="utf-8")
        self.assertIn("GOOS=windows GOARCH=amd64", build_script)
        self.assertIn("GOOS=windows GOARCH=arm64", build_script)
        self.assertIn("-H=windowsgui", build_script)
        checksums = dict(
            reversed(line.split())
            for line in (ROOT / "release/SHA256SUMS").read_text(encoding="utf-8").splitlines()
            if line.strip()
        )
        self.assertIn(
            checksums["warp-windows-helper-0.16.0-r2-amd64.exe"], windows
        )
        self.assertIn(
            checksums["warp-windows-helper-0.16.0-r2-arm64.exe"], windows
        )
        self.assertIn("warp-windows.exe", checksums)
        self.assertIn("warp-windows-arm64.exe", checksums)

    def test_region_probe_prepares_apk_destination(self):
        probe = (ROOT / "tools/test-direct-regions.sh").read_text(encoding="utf-8")
        self.assertIn('mkdir "$temp_dir/root"', probe)
        self.assertLess(
            probe.index('mkdir "$temp_dir/root"'),
            probe.index('apk --allow-untrusted extract'),
        )

    def test_temporary_scanner_checksums_match_release_manifest(self):
        manager = MANAGER.read_text(encoding="utf-8")
        manifest = (ROOT / "release/SHA256SUMS").read_text(encoding="utf-8")
        checksums = dict(
            reversed(line.split()) for line in manifest.splitlines() if line.strip()
        )
        expected = {
            "aarch64": checksums["warp-warpscout-0.16.0-r2-aarch64.apk"],
            "aarch64_cortex-a53": checksums[
                "warp-warpscout-0.16.0-r2-aarch64_cortex-a53.apk"
            ],
        }
        self.assertIn(f"SCOUT_SHA_AARCH64='{expected['aarch64']}'", manager)
        self.assertIn(f"SCOUT_SHA_A53='{expected['aarch64_cortex-a53']}'", manager)


if __name__ == "__main__":
    unittest.main(verbosity=2)
