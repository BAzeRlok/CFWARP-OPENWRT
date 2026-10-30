//go:build warp_windows_helper

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type windowsRoundTrip func(*http.Request) (*http.Response, error)

func (f windowsRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func probeReply(code int, body string) (*http.Response, error) {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
}

const validWindowsMeta = `{"country":"RU","colo":{"iata":"ARN","cca2":"SE","city":"Stockholm"}}`
const validWindowsTrace = "ip=104.28.230.247\ncolo=ARN\nloc=RU\nwarp=on\n"

func TestWindowsTraceRequiresRealWARPData(t *testing.T) {
	meta, err := parseWindowsTrace(validWindowsTrace)
	if err != nil || meta.colo != "ARN" || meta.loc != "RU" || meta.coloISO != "" {
		t.Fatalf("trace metadata must not confuse client and node country: %+v, %v", meta, err)
	}
	for _, body := range []string{
		"<html>blocked</html>", strings.Replace(validWindowsTrace, "warp=on", "warp=off", 1),
		strings.Replace(validWindowsTrace, "104.28.230.247", "192.168.1.1", 1),
		strings.Replace(validWindowsTrace, "ARN", "garbage", 1), validWindowsTrace + "warp=off\n",
		strings.Replace(validWindowsTrace, "loc=RU\n", "", 1),
	} {
		if _, err := parseWindowsTrace(body); err == nil {
			t.Fatalf("invalid proof accepted: %q", body)
		}
	}
}

func TestWindowsMetadataAndIndependentTrace(t *testing.T) {
	for _, first := range []string{"working", "dns failure", "bad JSON", "HTTP refusal", "too large"} {
		t.Run(first, func(t *testing.T) {
			windowsProbeLog.reset()
			traces := 0
			normal := &http.Client{Transport: windowsRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != metaURL || r.Header.Get("Referer") != metaReferer {
					t.Fatal("unexpected metadata request")
				}
				switch first {
				case "dns failure":
					return nil, &net.DNSError{Err: "blocked", Name: metaHost}
				case "bad JSON":
					return probeReply(200, "<html>challenge</html>")
				case "HTTP refusal":
					return probeReply(403, validWindowsMeta)
				case "too large":
					return probeReply(200, validWindowsMeta+strings.Repeat(" ", 8192))
				}
				return probeReply(200, validWindowsMeta)
			})}
			compat := &http.Client{Transport: windowsRoundTrip(func(r *http.Request) (*http.Response, error) {
				traces++
				if r.URL.String() != "https://1.1.1.1/cdn-cgi/trace" {
					t.Fatal("fallback must not require DNS")
				}
				return probeReply(200, validWindowsTrace)
			})}
			meta, err := windowsDataProof(context.Background(), normal, compat, "endpoint")
			if err != nil || meta.colo != "ARN" {
				t.Fatalf("meta=%+v err=%v", meta, err)
			}
			if first == "working" {
				if traces != 0 || meta.coloISO != "SE" {
					t.Fatal("healthy metadata lost")
				}
			} else if traces != 1 || meta.coloISO != "" {
				t.Fatal("fallback should preserve unknown node country")
			}
		})
	}
}

func TestWindowsAllHTTPSFailuresAreReported(t *testing.T) {
	var addresses []string
	client := &http.Client{Transport: windowsRoundTrip(func(r *http.Request) (*http.Response, error) {
		addresses = append(addresses, r.URL.String())
		if r.URL.Host == "1.1.1.1" {
			return probeReply(200, strings.Replace(validWindowsTrace, "warp=on", "warp=off", 1))
		}
		return nil, context.DeadlineExceeded
	})}
	_, err := windowsDataProof(context.Background(), client, client, "endpoint")
	if err == nil || len(addresses) != 3 || !strings.Contains(err.Error(), "speed.cloudflare.com") || !strings.Contains(err.Error(), "1.1.1.1") || !strings.Contains(err.Error(), "1.0.0.1") {
		t.Fatalf("addresses=%v err=%v", addresses, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = windowsDataProof(ctx, client, client, "endpoint")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestWindowsHandshakeAndICMPNeverReplaceHTTPS(t *testing.T) {
	for _, scenario := range []string{"handshake failed", "https failed", "live", "icmp blocked", "torn down"} {
		t.Run(scenario, func(t *testing.T) {
			windowsProbeLog.reset()
			dataCalls, pingCalls := 0, 0
			ops := tunnelProbeOps{
				handshake: func(context.Context, string, time.Duration) bool { return scenario != "handshake failed" },
				data: func(context.Context) (metaResult, error) {
					dataCalls++
					if scenario == "https failed" || scenario == "torn down" && dataCalls == 2 {
						return metaResult{}, context.DeadlineExceeded
					}
					return metaResult{colo: "ARN", coloISO: "SE"}, nil
				},
				durability: func(int, time.Duration) (time.Duration, float32, bool) {
					pingCalls++
					return time.Millisecond, 1, scenario == "icmp blocked" || scenario == "torn down"
				},
			}
			_, _, _, _, ok := verifyWindowsProbe(context.Background(), "endpoint", time.Second, 10, ops)
			want := scenario == "live" || scenario == "icmp blocked"
			if ok != want {
				t.Fatalf("accepted=%v, want=%v", ok, want)
			}
			if scenario == "handshake failed" && dataCalls != 0 {
				t.Fatal("HTTP sent without a handshake")
			}
			if scenario == "https failed" && pingCalls != 0 {
				t.Fatal("ping must not replace failed HTTPS proof")
			}
			if (scenario == "icmp blocked" || scenario == "torn down") && dataCalls != 2 {
				t.Fatal("missing second data proof")
			}
		})
	}
}

func TestWindowsProbeTLSAndTunnelDial(t *testing.T) {
	dials := 0
	normal, compat := newWindowsProbeClients(func(context.Context, string) (net.Conn, error) { dials++; return nil, context.DeadlineExceeded })
	for _, client := range []*http.Client{normal, compat} {
		transport := client.Transport.(*http.Transport)
		if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
			t.Fatal("unsafe HTTP probe transport")
		}
		_, err := transport.DialContext(context.Background(), "tcp", "1.1.1.1:443")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("tunnel dialer bypassed")
		}
		if client.CheckRedirect(nil, nil) == nil {
			t.Fatal("probe redirects accepted")
		}
	}
	if dials != 2 || compat.Transport.(*http.Transport).TLSClientConfig.MaxVersion != tls.VersionTLS12 {
		t.Fatal("missing compatibility transport")
	}
}
