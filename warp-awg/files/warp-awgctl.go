// SPDX-License-Identifier: MIT
// Minimal, non-interactive UAPI client for the private WARP AmneziaWG daemon.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var interfaceName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,14}$`)

type awgConfig struct {
	privateKey string
	publicKey  string
	endpoint   string
	allowedIPs []string
	keepalive  uint64
	jc         uint64
	jmin       uint64
	jmax       uint64
	i1         string
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func keyHex(value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return "", errors.New("key must be 32-byte base64")
	}
	return hex.EncodeToString(raw), nil
}

func uintValue(name, value string, max uint64) (uint64, error) {
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n > max {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return n, nil
}

func parseEndpoint(value string) error {
	host, portText, err := net.SplitHostPort(value)
	if err != nil || net.ParseIP(host) == nil {
		return errors.New("endpoint must contain a numeric IP address and port")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return errors.New("endpoint port is invalid")
	}
	return nil
}

func parseConfig(path string) (awgConfig, error) {
	var cfg awgConfig
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()

	section := ""
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			if section != "interface" && section != "peer" {
				return cfg, fmt.Errorf("unsupported section %q", section)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			return cfg, fmt.Errorf("invalid configuration line")
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return cfg, fmt.Errorf("empty or unsafe %s value", key)
		}

		switch section + "." + key {
		case "interface.privatekey":
			cfg.privateKey = value
		case "interface.jc":
			cfg.jc, err = uintValue("Jc", value, 65535)
		case "interface.jmin":
			cfg.jmin, err = uintValue("Jmin", value, 65535)
		case "interface.jmax":
			cfg.jmax, err = uintValue("Jmax", value, 65535)
		case "interface.i1":
			if len(value) > 8192 {
				err = errors.New("I1 is too long")
			} else {
				cfg.i1 = value
			}
		case "peer.publickey":
			cfg.publicKey = value
		case "peer.endpoint":
			cfg.endpoint = value
			err = parseEndpoint(value)
		case "peer.allowedips":
			for _, prefixText := range strings.Split(value, ",") {
				prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(prefixText))
				if parseErr != nil {
					err = errors.New("invalid AllowedIPs")
					break
				}
				cfg.allowedIPs = append(cfg.allowedIPs, prefix.String())
			}
		case "peer.persistentkeepalive":
			cfg.keepalive, err = uintValue("PersistentKeepalive", value, 65535)
		default:
			return cfg, fmt.Errorf("unsupported option %s in [%s]", key, section)
		}
		if err != nil {
			return cfg, err
		}
	}
	if err := scanner.Err(); err != nil {
		return cfg, err
	}
	if _, err := keyHex(cfg.privateKey); err != nil {
		return cfg, fmt.Errorf("private key: %w", err)
	}
	if _, err := keyHex(cfg.publicKey); err != nil {
		return cfg, fmt.Errorf("public key: %w", err)
	}
	if cfg.endpoint == "" || len(cfg.allowedIPs) == 0 || cfg.jc == 0 || cfg.jmax < cfg.jmin || cfg.i1 == "" {
		return cfg, errors.New("configuration is incomplete")
	}
	return cfg, nil
}

func setPayload(cfg awgConfig) (string, error) {
	privateKey, err := keyHex(cfg.privateKey)
	if err != nil {
		return "", err
	}
	publicKey, err := keyHex(cfg.publicKey)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "set=1\nprivate_key=%s\nreplace_peers=true\n", privateKey)
	fmt.Fprintf(&b, "jc=%d\njmin=%d\njmax=%d\ni1=%s\n", cfg.jc, cfg.jmin, cfg.jmax, cfg.i1)
	fmt.Fprintf(&b, "public_key=%s\nendpoint=%s\n", publicKey, cfg.endpoint)
	fmt.Fprintf(&b, "persistent_keepalive_interval=%d\nreplace_allowed_ips=true\n", cfg.keepalive)
	for _, prefix := range cfg.allowedIPs {
		fmt.Fprintf(&b, "allowed_ip=%s\n", prefix)
	}
	b.WriteByte('\n')
	return b.String(), nil
}

func exchange(iface, payload string) (map[string][]string, error) {
	if !interfaceName.MatchString(iface) {
		return nil, errors.New("invalid interface name")
	}
	socketPath := filepath.Join("/var/run/amneziawg", iface+".sock")
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(payload)); err != nil {
		return nil, err
	}

	values := make(map[string][]string)
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errors.New("invalid UAPI response")
		}
		values[key] = append(values[key], value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	errno := values["errno"]
	if len(errno) != 1 || errno[0] != "0" {
		return nil, fmt.Errorf("UAPI rejected request (errno=%s)", strings.Join(errno, ","))
	}
	return values, nil
}

func doSetConf(iface, path string) error {
	cfg, err := parseConfig(path)
	if err != nil {
		return err
	}
	payload, err := setPayload(cfg)
	if err != nil {
		return err
	}
	_, err = exchange(iface, payload)
	return err
}

func doGet(iface, field string) error {
	allowed := map[string]bool{
		"endpoint": true, "last_handshake_time_sec": true,
		"rx_bytes": true, "tx_bytes": true, "listen_port": true,
	}
	if !allowed[field] {
		return errors.New("unsupported status field")
	}
	values, err := exchange(iface, "get=1\n\n")
	if err != nil {
		return err
	}
	if values := values[field]; len(values) > 0 {
		fmt.Println(values[len(values)-1])
	}
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: warp-awgctl setconf INTERFACE FILE | get INTERFACE FIELD")
}

func main() {
	var err error
	switch {
	case len(os.Args) == 4 && os.Args[1] == "setconf":
		err = doSetConf(os.Args[2], os.Args[3])
	case len(os.Args) == 4 && os.Args[1] == "get":
		err = doGet(os.Args[2], os.Args[3])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fail("warp-awgctl: %v", err)
	}
}
