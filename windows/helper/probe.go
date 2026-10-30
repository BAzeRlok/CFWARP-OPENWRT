//go:build warp_windows_helper

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const windowsHTTPTimeout = 4 * time.Second

type tunnelProbeOps struct {
	handshake  func(context.Context, string, time.Duration) bool
	data       func(context.Context) (metaResult, error)
	durability func(int, time.Duration) (time.Duration, float32, bool)
}

func probeWindowsEndpoint(ctx context.Context, t tunnel, endpoint string, timeout time.Duration, pings int, wantMeta bool) (metaResult, time.Duration, float32, bool, bool) {
	st := t.stack()
	normal, compat := newWindowsProbeClients(func(ctx context.Context, address string) (net.Conn, error) {
		// Never use the host's resolver, proxy or sockets for proof of a tunnel.
		return st.tnet.DialContext(ctx, "tcp4", address)
	})
	defer normal.CloseIdleConnections()
	defer compat.CloseIdleConnections()
	ops := tunnelProbeOps{handshake: t.handshake, durability: st.durability, data: func(ctx context.Context) (metaResult, error) {
		return windowsDataProof(ctx, normal, compat, endpoint)
	}}
	if !wantMeta {
		if !ops.handshake(ctx, endpoint, timeout) {
			return metaResult{}, 0, 0, false, false
		}
		if pings == 0 {
			return metaResult{}, 0, 0, false, true
		}
		rtt, loss, torn := ops.durability(pings, timeout)
		return metaResult{}, rtt, loss, torn, true
	}
	return verifyWindowsProbe(ctx, endpoint, timeout, pings, ops)
}

func verifyWindowsProbe(ctx context.Context, endpoint string, timeout time.Duration, pings int, ops tunnelProbeOps) (metaResult, time.Duration, float32, bool, bool) {
	if !ops.handshake(ctx, endpoint, timeout) {
		if ctx.Err() == nil {
			windowsProbeLog.record("handshake", endpoint, errors.New("no new WireGuard handshake before timeout"))
		}
		return metaResult{}, 0, 0, false, false
	}
	meta, err := ops.data(ctx)
	if err != nil {
		if ctx.Err() == nil {
			windowsProbeLog.record("https", endpoint, err)
		}
		return metaResult{}, 0, 0, false, false
	}
	var rtt time.Duration
	var loss float32
	if pings > 0 {
		var torn bool
		rtt, loss, torn = ops.durability(pings, timeout)
		if torn {
			// ICMP may be filtered even though HTTPS works. Re-check actual
			// application data rather than accepting a stale handshake/ping.
			if _, err := ops.data(ctx); err != nil {
				windowsProbeLog.record("data_lost", endpoint, err)
				return meta, rtt, loss, true, false
			}
			windowsProbeLog.record("icmp_unavailable", endpoint, errors.New("ICMP durability failed but a second HTTPS request passed"))
		}
	}
	windowsProbeLog.record("verified", endpoint, nil)
	return meta, rtt, loss, false, true
}

func newWindowsProbeClients(dial func(context.Context, string) (net.Conn, error)) (*http.Client, *http.Client) {
	makeClient := func(compatibility bool) *http.Client {
		config := &tls.Config{MinVersion: tls.VersionTLS12}
		if compatibility {
			config.MaxVersion = tls.VersionTLS12
		}
		return &http.Client{
			Transport: &http.Transport{
				Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: true,
				TLSClientConfig: config, TLSHandshakeTimeout: windowsHTTPTimeout,
				ResponseHeaderTimeout: windowsHTTPTimeout,
				DialContext:           func(ctx context.Context, _, address string) (net.Conn, error) { return dial(ctx, address) },
			},
			Timeout:       windowsHTTPTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("probe redirects are not accepted") },
		}
	}
	return makeClient(false), makeClient(true)
}

func fetchWindowsProbe(ctx context.Context, client *http.Client, address string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, windowsHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	if address == metaURL {
		request.Header.Set("Referer", metaReferer)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d", address, response.StatusCode)
	}
	const maximum = 8192
	body, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return "", err
	}
	if len(body) > maximum {
		return "", fmt.Errorf("%s: response too large", address)
	}
	return string(body), nil
}

func upperCode(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, ch := range value {
		if ch < 'A' || ch > 'Z' {
			return false
		}
	}
	return true
}

func parseWindowsTrace(body string) (metaResult, error) {
	fields := make(map[string]string)
	for _, line := range strings.Split(body, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		if _, duplicate := fields[key]; duplicate {
			return metaResult{}, errors.New("duplicate trace field")
		}
		fields[key] = value
	}
	ip, err := netip.ParseAddr(fields["ip"])
	if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() ||
		(fields["warp"] != "on" && fields["warp"] != "plus") ||
		!upperCode(fields["colo"], 3) || !upperCode(fields["loc"], 2) {
		return metaResult{}, errors.New("trace does not confirm WARP application traffic")
	}
	// loc describes the observed client location, not the colo's country.
	// Leave coloISO unknown; explicit country filters must reject unknowns.
	return metaResult{loc: fields["loc"], colo: fields["colo"]}, nil
}

func windowsDataProof(ctx context.Context, normal, compat *http.Client, endpoint string) (metaResult, error) {
	body, err := fetchWindowsProbe(ctx, normal, metaURL)
	if err == nil {
		meta := parseMeta(body)
		if upperCode(meta.colo, 3) && upperCode(meta.coloISO, 2) && upperCode(meta.loc, 2) {
			return meta, nil
		}
		err = errors.New("speed.cloudflare.com/meta: invalid node metadata")
	}
	failures := []error{err}
	for _, address := range []string{"https://1.1.1.1/cdn-cgi/trace", "https://1.0.0.1/cdn-cgi/trace"} {
		if ctx.Err() != nil {
			return metaResult{}, ctx.Err()
		}
		body, fetchErr := fetchWindowsProbe(ctx, compat, address)
		if fetchErr == nil {
			meta, parseErr := parseWindowsTrace(body)
			if parseErr == nil {
				windowsProbeLog.record("trace_fallback", endpoint, fmt.Errorf("metadata unavailable (%v); verified HTTPS trace passed", failures[0]))
				return meta, nil
			}
			fetchErr = fmt.Errorf("%s: %w", address, parseErr)
		}
		failures = append(failures, fetchErr)
	}
	return metaResult{}, errors.Join(failures...)
}
