package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testDNSQuery() []byte {
	return []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
}

func testDNSAnswer(query []byte) []byte {
	response := append([]byte(nil), query...)
	response[2], response[3] = 0x81, 0x80
	response[6], response[7] = 0, 1
	return append(response, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 1)
}

func TestStandaloneUDPAndDualStackListeners(t *testing.T) {
	config := dnsConfig{Enabled: true, DNSOnly: true, Type: "udp", Address: "8.8.8.8"}
	ordinary := config
	ordinary.DNSOnly = false
	if _, err := newDNSProxy(ordinary); err == nil {
		t.Fatal("ordinary UDP DNS must use the native tunnel settings")
	}
	upstream, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	tcpUpstream, err := net.Listen("tcp4", upstream.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tcpUpstream.Close()
	var truncate atomic.Bool
	var tcpQueries atomic.Int32
	go func() {
		buffer := make([]byte, maxDNSMessage)
		for {
			n, address, err := upstream.ReadFrom(buffer)
			if err != nil {
				return
			}
			response := testDNSAnswer(buffer[:n])
			if truncate.Load() {
				response = dnsErrorResponse(buffer[:n], true)
			}
			upstream.WriteTo(response, address)
		}
	}()
	go func() {
		for {
			conn, err := tcpUpstream.Accept()
			if err != nil {
				return
			}
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			query, err := readDNSFrame(conn)
			if err == nil {
				tcpQueries.Add(1)
				writeDNSFrame(conn, testDNSAnswer(query))
			}
			conn.Close()
		}
	}()
	proxy, err := newDNSProxy(config)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxy.host, proxy.port, _ = net.SplitHostPort(upstream.LocalAddr().String())
	proxy.config.BootstrapIPs = []string{proxy.host}
	if err = proxy.listenAt("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{proxy.udp.LocalAddr().String(), proxy.udp6.LocalAddr().String()} {
		for _, network := range []string{"udp", "tcp"} {
			for _, truncated := range []bool{false, true} {
				truncate.Store(truncated)
				conn, err := net.DialTimeout(network, address, 3*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				query := testDNSQuery()
				var response []byte
				if network == "tcp" {
					err = writeDNSFrame(conn, query)
					if err == nil {
						response, err = readDNSFrame(conn)
					}
				} else {
					_, err = conn.Write(query)
					if err == nil {
						buffer := make([]byte, 512)
						var n int
						n, err = conn.Read(buffer)
						response = buffer[:n]
					}
				}
				conn.Close()
				if err != nil || !bytes.Equal(response, testDNSAnswer(query)) {
					t.Fatalf("%s %s truncated=%v: response=%x err=%v", network, address, truncated, response, err)
				}
			}
		}
	}
	if tcpQueries.Load() != 4 {
		t.Fatalf("upstream TCP retries = %d, want 4", tcpQueries.Load())
	}
	proxy.Close()
	for _, address := range []string{proxy.tcp.Addr().String(), proxy.tcp6.Addr().String()} {
		if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
			conn.Close()
			t.Fatal("DNS listener remains after Close")
		}
	}
}

func testTLSCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "resolver.invalid"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"resolver.invalid"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func testDoHProxy(t *testing.T, handler http.Handler) *dnsProxy {
	t.Helper()
	cert, roots := testTLSCertificate(t)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	proxy, err := newDNSProxy(dnsConfig{Enabled: true, Type: "doh", Address: "https://resolver.invalid:" + port + "/dns-query", BootstrapIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	proxy.tlsConfig.RootCAs = roots
	if err := proxy.listenAt("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.Close)
	return proxy
}

func localDNSExchange(t *testing.T, proxy *dnsProxy, network string, query []byte) []byte {
	t.Helper()
	conn, err := net.DialTimeout(network, proxy.udp.LocalAddr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if network == "tcp" {
		if err := writeDNSFrame(conn, query); err != nil {
			t.Fatal(err)
		}
		response, err := readDNSFrame(conn)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	if _, err := conn.Write(query); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, maxDNSMessage)
	n, err := conn.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	return buffer[:n]
}

func TestDNSConfiguration(t *testing.T) {
	if got := defaultDNSConfig(); got.Enabled || got.Type != "doh" || got.Address != "dns.zapret.moe/dns-query" {
		t.Fatalf("unexpected defaults: %+v", got)
	}
	for _, entry := range []struct {
		kind, address string
		valid         bool
	}{
		{"udp", "1.1.1.1", true}, {"udp", "udp://8.8.8.8:53", true},
		{"udp", "2606:4700:4700::1111", true}, {"udp", "[2606:4700:4700::1111]:53", true},
		{"udp", "192.168.1.1", true}, {"udp", "dns.example", false},
		{"udp", "1.1.1.1:5353", false}, {"udp", "127.0.0.1", false},
		{"udp", "::1", false}, {"udp", "0.0.0.0", false}, {"udp", "224.0.0.1", false},
		{"dot", "dns.example", true}, {"dot", "tls://dns.example:8853", true},
		{"dot", "dot://[2606:4700:4700::1111]:853", true},
		{"dot", "dns.example/path", false}, {"dot", "dns.example?query", false},
		{"dot", "dns.example#fragment", false}, {"dot", "user@dns.example", false},
		{"dot", "dns.example:0", false}, {"dot", "dns.example:https", false},
		{"dot", "dns.example:65536", false}, {"dot", "https://dns.example", false},
		{"doh", "dns.zapret.moe/dns-query", true}, {"doh", "https://dns.example", true},
		{"doh", "https://[2606:4700:4700::1111]/dns-query", true},
		{"doh", "http://dns.example/dns-query", false}, {"doh", "https://user:pass@dns.example/", false},
		{"doh", "https://dns.example/#fragment", false}, {"doh", "https://dns.example:0/", false},
		{"doh", "https://dns.example:/", false}, {"other", "dns.example", false},
	} {
		t.Run(entry.kind+"/"+entry.address, func(t *testing.T) {
			err := validateDNSConfig(dnsConfig{Enabled: true, Type: entry.kind, Address: entry.address})
			if (err == nil) != entry.valid {
				t.Fatalf("valid=%v, error=%v", entry.valid, err)
			}
		})
	}
	if address, err := parseDoHURL("dns.example"); err != nil || address.String() != "https://dns.example/dns-query" {
		t.Fatalf("DoH shorthand: %v, %v", address, err)
	}
	for _, entry := range []struct {
		config dnsConfig
		want   string
	}{
		{defaultDNSConfig(), "8.8.8.8, 8.8.4.4"},
		{dnsConfig{Enabled: true, Type: "udp", Address: "1.1.1.1:53"}, "1.1.1.1"},
		{dnsConfig{Enabled: true, Type: "doh", Address: defaultDNSURL}, "127.0.0.1"},
	} {
		if got := dnsServersForConfig(entry.config); got != entry.want {
			t.Fatalf("DNS=%q, expected %q", got, entry.want)
		}
	}
}

func TestPrepareDNSLiteralAndBlockedAddresses(t *testing.T) {
	config, err := prepareDNSConfig(context.Background(), dnsConfig{Enabled: true, Type: " DoT ", Address: " 1.1.1.1 "})
	if err != nil || config.Type != "dot" || config.Address != "1.1.1.1" || len(config.BootstrapIPs) != 1 || config.BootstrapIPs[0] != "1.1.1.1" {
		t.Fatalf("prepare: %+v, %v", config, err)
	}
	for _, address := range []string{"127.0.0.1", "::1", "0.0.0.0", "224.0.0.1", "169.254.1.1", "fe80::1"} {
		if _, err := prepareDNSConfig(context.Background(), dnsConfig{Enabled: true, Type: "dot", Address: address}); err == nil {
			t.Fatalf("accepted unusable bootstrap address %s", address)
		}
	}
}

func TestDoHLocalUDPAndTCP(t *testing.T) {
	var calls atomic.Int32
	proxy := testDoHProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/dns-message" || r.ProtoMajor != 2 || r.Host[:len("resolver.invalid")] != "resolver.invalid" {
			t.Errorf("unexpected DoH request: %s, %s, %s", r.Method, r.Proto, r.Host)
		}
		query, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(testDNSAnswer(query))
	}))
	query := testDNSQuery()
	for _, network := range []string{"udp", "tcp"} {
		if got := localDNSExchange(t, proxy, network, query); !bytes.Equal(got, testDNSAnswer(query)) {
			t.Fatalf("%s answer: %x", network, got)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream requests: %d", calls.Load())
	}
}

func TestDoHErrorsBecomeSERVFAIL(t *testing.T) {
	for _, failure := range []string{"id", "qr", "short", "oversized", "type", "status", "redirect"} {
		t.Run(failure, func(t *testing.T) {
			proxy := testDoHProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query, _ := io.ReadAll(r.Body)
				response := testDNSAnswer(query)
				w.Header().Set("Content-Type", "application/dns-message")
				switch failure {
				case "id":
					response[0] ^= 1
				case "qr":
					response[2] &^= 0x80
				case "short":
					response = []byte{1, 2}
				case "oversized":
					response = bytes.Repeat([]byte{1}, maxDNSMessage+1)
				case "type":
					w.Header().Set("Content-Type", "text/html")
				case "status":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "redirect":
					w.Header().Set("Location", "https://must-not-resolve.invalid/dns-query")
					w.WriteHeader(http.StatusTemporaryRedirect)
				}
				w.Write(response)
			}))
			query := testDNSQuery()
			response := localDNSExchange(t, proxy, "udp", query)
			if response[3]&0xf != 2 || response[2]&0x80 == 0 || !bytes.Equal(response[12:], query[12:]) {
				t.Fatalf("expected SERVFAIL with original question, got %x", response)
			}
		})
	}
}

func TestUDPTruncationAndTCPFullResponse(t *testing.T) {
	proxy := testDoHProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		response := testDNSAnswer(query)
		response = append(response, make([]byte, 700)...)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(response)
	}))
	query := testDNSQuery()
	if response := localDNSExchange(t, proxy, "udp", query); len(response) > 512 || response[2]&2 == 0 || response[3]&0xf != 0 {
		t.Fatalf("UDP should set TC, got %x", response)
	}
	if response := localDNSExchange(t, proxy, "tcp", query); len(response) <= 512 || response[2]&2 != 0 {
		t.Fatalf("TCP should return the full reply, got %d bytes", len(response))
	}
	query[11] = 1
	query = append(query, 0, 0, 41, 0x04, 0xd0, 0, 0, 0, 0, 0, 0)
	if dnsUDPSize(query) != 1232 {
		t.Fatal("EDNS buffer size not parsed")
	}
}

func TestDoTTLSFramingAndConnectionReuse(t *testing.T) {
	certificate, roots := testTLSCertificate(t)
	listener, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var connections atomic.Int32
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				for {
					query, err := readDNSFrame(conn)
					if err != nil {
						return
					}
					if err := writeDNSFrame(conn, testDNSAnswer(query)); err != nil {
						return
					}
				}
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	proxy, err := newDNSProxy(dnsConfig{Enabled: true, Type: "dot", Address: "resolver.invalid:" + port, BootstrapIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	proxy.tlsConfig.RootCAs = roots
	defer func() { proxy.Close(); listener.Close(); workers.Wait() }()
	if err := proxy.listenAt("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{"udp", "tcp", "udp"} {
		if response := localDNSExchange(t, proxy, network, testDNSQuery()); !bytes.Equal(response, testDNSAnswer(testDNSQuery())) {
			t.Fatalf("DoT reply: %x", response)
		}
	}
	if connections.Load() != 1 {
		t.Fatalf("expected one reused DoT connection, got %d", connections.Load())
	}
}

func TestTLSNameVerification(t *testing.T) {
	proxy := testDoHProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached upstream despite incorrect TLS name")
	}))
	proxy.tlsConfig.ServerName = "incorrect.invalid"
	if _, err := proxy.forward(context.Background(), testDNSQuery()); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected certificate verification error, got %v", err)
	}
}

func TestCloseCancelsInFlightAndIdleTCP(t *testing.T) {
	started := make(chan struct{})
	proxy := testDoHProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	udp, err := net.Dial("udp4", proxy.udp.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	udp.Write(testDNSQuery())
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request did not start")
	}
	idle, err := net.Dial("tcp4", proxy.udp.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	idle.SetDeadline(time.Now().Add(time.Second))
	idle.Write([]byte{0}) // A partial frame must not block service shutdown.
	start := time.Now()
	proxy.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("shutdown blocked for %s", elapsed)
	}
	var buffer [1]byte
	if _, err := idle.Read(buffer[:]); err == nil {
		t.Fatal("idle DNS client connection not closed")
	}
	proxy.Close() // Idempotent.
}

func TestDNSFrameAndPacketBounds(t *testing.T) {
	for _, size := range []int{0, 11, maxDNSMessage + 1} {
		if err := writeDNSFrame(io.Discard, make([]byte, size)); err == nil {
			t.Fatalf("accepted frame size %d", size)
		}
	}
	if _, err := readDNSFrame(bytes.NewReader([]byte{0, 11})); err == nil {
		t.Fatal("accepted undersized frame")
	}
	query := testDNSQuery()
	for _, mutation := range []func([]byte){
		func(q []byte) { q[2] |= 0x80 },
		func(q []byte) { q[5] = 0 },
		func(q []byte) { q[7] = 1 },
		func(q []byte) { q[12] = 64 },
	} {
		invalid := append([]byte(nil), query...)
		mutation(invalid)
		if validateDNSQuery(invalid) == nil {
			t.Fatalf("accepted malformed query %x", invalid)
		}
	}
	for _, failure := range []string{"id", "qr", "short", "size"} {
		response := testDNSAnswer(query)
		switch failure {
		case "id":
			response[1]++
		case "qr":
			response[2] &^= 0x80
		case "short":
			response = response[:11]
		case "size":
			response = append(response, make([]byte, maxDNSMessage)...)
		}
		if validateDNSResponse(query, response) == nil {
			t.Fatalf("accepted bad response: %s", failure)
		}
	}
	response := dnsErrorResponse(query, false)
	if binary.BigEndian.Uint16(response[:2]) != binary.BigEndian.Uint16(query[:2]) {
		t.Fatal("SERVFAIL lost query ID")
	}
}
