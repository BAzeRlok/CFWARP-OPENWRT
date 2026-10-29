package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultDNSType = "doh"
	defaultDNSURL  = "dns.zapret.moe/dns-query"
	maxDNSMessage  = 65535
	dnsTimeout     = 10 * time.Second
)

type dnsConfig struct {
	Enabled      bool     `json:"enabled"`
	Type         string   `json:"type"`
	Address      string   `json:"address"`
	BootstrapIPs []string `json:"bootstrap_ips,omitempty"`
}

func defaultDNSConfig() dnsConfig {
	return dnsConfig{Type: defaultDNSType, Address: defaultDNSURL}
}

func usableDNSIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsLoopback()
}

func validDNSHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func validDNSPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535 && strconv.Itoa(n) == port
}

func parseDNSHostPort(value, defaultPort string, prefixes ...string) (string, string, error) {
	value = strings.TrimSpace(value)
	for _, prefix := range prefixes {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = value[len(prefix):]
			break
		}
	}
	if strings.ContainsAny(value, "/?#@%\\ \t\r\n") || value == "" {
		return "", "", errors.New("некорректный адрес DNS-сервера")
	}
	host, port := value, defaultPort
	if ip := net.ParseIP(value); ip != nil {
		host = ip.String() // An unbracketed IPv6 literal has no explicit port.
	} else if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
		if net.ParseIP(host) == nil {
			return "", "", errors.New("некорректный IPv6-адрес DNS-сервера")
		}
	} else if strings.Contains(value, ":") {
		var err error
		host, port, err = net.SplitHostPort(value)
		if err != nil {
			return "", "", errors.New("некорректный адрес или порт DNS-сервера")
		}
	}
	if !validDNSHost(host) || !validDNSPort(port) {
		return "", "", errors.New("некорректный адрес или порт DNS-сервера")
	}
	return strings.ToLower(host), port, nil
}

func parseDoHURL(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Fragment != "" || strings.Contains(value, "#") || !validDNSHost(u.Hostname()) {
		return nil, errors.New("DoH должен использовать HTTPS-адрес без имени пользователя и фрагмента")
	}
	if _, _, err := parseDNSHostPort(u.Host, "443"); err != nil {
		return nil, err
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/dns-query"
	}
	return u, nil
}

func validateDNSConfig(config dnsConfig) error {
	if !config.Enabled {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(config.Type)) {
	case "udp":
		host, port, err := parseDNSHostPort(config.Address, "53", "udp://")
		if err != nil {
			return err
		}
		if !usableDNSIP(net.ParseIP(host)) || port != "53" {
			return errors.New("для UDP укажите IP DNS-сервера с обычным портом 53")
		}
	case "dot":
		_, _, err := parseDNSHostPort(config.Address, "853", "tls://", "dot://")
		return err
	case "doh":
		_, err := parseDoHURL(config.Address)
		return err
	default:
		return errors.New("тип DNS должен быть UDP, DoT или DoH")
	}
	return nil
}

// Resolve before replacing the tunnel's DNS with localhost. Once persisted,
// these addresses also let the service start after reboot without DNS recursion.
func prepareDNSConfig(ctx context.Context, config dnsConfig) (dnsConfig, error) {
	config.Type = strings.ToLower(strings.TrimSpace(config.Type))
	config.Address = strings.TrimSpace(config.Address)
	config.BootstrapIPs = nil
	if err := validateDNSConfig(config); err != nil || !config.Enabled || config.Type == "udp" {
		return config, err
	}
	host, _, err := dnsEndpoint(config)
	if err != nil {
		return config, err
	}
	var addresses []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		addresses = []net.IPAddr{{IP: ip}}
	} else {
		ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
		defer cancel()
		addresses, err = net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return config, fmt.Errorf("не удалось определить IP DNS-сервера: %w", err)
		}
	}
	seen := make(map[string]bool)
	// Prefer IPv4 on networks without working IPv6, retaining both families.
	for _, ipv4 := range []bool{true, false} {
		for _, address := range addresses {
			ip := address.IP
			if !usableDNSIP(ip) || (ip.To4() != nil) != ipv4 || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			config.BootstrapIPs = append(config.BootstrapIPs, ip.String())
		}
	}
	if len(config.BootstrapIPs) == 0 {
		return config, errors.New("у DNS-сервера нет подходящих IP-адресов")
	}
	return config, nil
}

func dnsServersForConfig(config dnsConfig) string {
	if !config.Enabled {
		return "8.8.8.8, 8.8.4.4"
	}
	if strings.EqualFold(config.Type, "udp") {
		host, _, _ := parseDNSHostPort(config.Address, "53", "udp://")
		return host
	}
	return "127.0.0.1"
}

func dnsEndpoint(config dnsConfig) (string, string, error) {
	if config.Type == "doh" {
		u, err := parseDoHURL(config.Address)
		if err != nil {
			return "", "", err
		}
		return parseDNSHostPort(u.Host, "443")
	}
	return parseDNSHostPort(config.Address, "853", "tls://", "dot://")
}

type dnsProxy struct {
	config      dnsConfig
	host        string
	port        string
	dohURL      string
	tlsConfig   *tls.Config
	http        *http.Client
	dotPool     chan net.Conn
	udp         *net.UDPConn
	tcp         net.Listener
	ctx         context.Context
	cancel      context.CancelFunc
	semaphore   chan struct{}
	workers     sync.WaitGroup
	closeOnce   sync.Once
	mu          sync.Mutex
	closing     bool
	connections map[net.Conn]struct{}
	nextAddress atomic.Uint32
}

func newDNSProxy(config dnsConfig) (*dnsProxy, error) {
	if err := validateDNSConfig(config); err != nil {
		return nil, err
	}
	if !config.Enabled || config.Type != "doh" && config.Type != "dot" {
		return nil, errors.New("локальная служба нужна только для DoH или DoT")
	}
	if len(config.BootstrapIPs) == 0 {
		return nil, errors.New("адрес DNS-сервера не сохранён; примените настройки DNS ещё раз")
	}
	for _, value := range config.BootstrapIPs {
		if net.ParseIP(value) == nil {
			return nil, errors.New("некорректный сохранённый IP DNS-сервера")
		}
	}
	host, port, err := dnsEndpoint(config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &dnsProxy{
		config: config, host: host, port: port, ctx: ctx, cancel: cancel,
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
		dotPool:   make(chan net.Conn, 8), semaphore: make(chan struct{}, 128),
		connections: make(map[net.Conn]struct{}),
	}
	if config.Type == "doh" {
		u, _ := parseDoHURL(config.Address)
		p.dohURL = u.String()
		p.http = &http.Client{
			Transport: &http.Transport{
				Proxy: nil, DialContext: p.dialContext, TLSClientConfig: p.tlsConfig,
				ForceAttemptHTTP2: true, MaxIdleConns: 8, MaxIdleConnsPerHost: 8,
				MaxConnsPerHost: 16, IdleConnTimeout: 60 * time.Second,
				TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: dnsTimeout,
				MaxResponseHeaderBytes: 16384, DisableCompression: true,
			},
			Timeout: dnsTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("перенаправления DoH запрещены; укажите конечный адрес")
			},
		}
	}
	return p, nil
}

func startDNSProxy(config dnsConfig) (*dnsProxy, error) {
	return startDNSProxyAt(config, "127.0.0.1:53")
}

func startDNSProxyAt(config dnsConfig, address string) (*dnsProxy, error) {
	p, err := newDNSProxy(config)
	if err != nil {
		return nil, err
	}
	if err := p.listenAt(address); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *dnsProxy) listenAt(address string) error {
	addr, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		return err
	}
	p.udp, err = net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("не удалось открыть локальный DNS UDP: %w", err)
	}
	p.tcp, err = net.Listen("tcp4", p.udp.LocalAddr().String())
	if err != nil {
		p.udp.Close()
		return fmt.Errorf("не удалось открыть локальный DNS TCP: %w", err)
	}
	p.workers.Add(2)
	go p.serveUDP()
	go p.serveTCP()
	return nil
}

func (p *dnsProxy) Close() {
	p.closeOnce.Do(func() {
		p.cancel()
		if p.udp != nil {
			p.udp.Close()
		}
		if p.tcp != nil {
			p.tcp.Close()
		}
		p.mu.Lock()
		p.closing = true
		connections := make([]net.Conn, 0, len(p.connections))
		for conn := range p.connections {
			connections = append(connections, conn)
		}
		p.mu.Unlock()
		for _, conn := range connections {
			conn.Close()
		}
		if p.http != nil {
			p.http.CloseIdleConnections()
		}
		p.workers.Wait()
	})
}

func (p *dnsProxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		conn.Close()
		return false
	}
	p.connections[conn] = struct{}{}
	return true
}

func (p *dnsProxy) untrack(conn net.Conn) {
	p.mu.Lock()
	delete(p.connections, conn)
	p.mu.Unlock()
}

type trackedDNSConn struct {
	net.Conn
	proxy *dnsProxy
}

func (conn *trackedDNSConn) Close() error {
	err := conn.Conn.Close()
	conn.proxy.untrack(conn.Conn)
	return err
}

func (p *dnsProxy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !strings.EqualFold(host, p.host) || port != p.port {
		return nil, errors.New("неожиданный адрес DNS-сервера")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)
	defer func() { stop(); cancel() }()
	start := int(p.nextAddress.Add(1)-1) % len(p.config.BootstrapIPs)
	var lastError error
	for offset := 0; offset < len(p.config.BootstrapIPs); offset++ {
		if ctx.Err() != nil || p.ctx.Err() != nil {
			return nil, context.Canceled
		}
		ip := p.config.BootstrapIPs[(start+offset)%len(p.config.BootstrapIPs)]
		dialer := net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		if err != nil {
			lastError = err
			continue
		}
		if !p.track(conn) {
			return nil, context.Canceled
		}
		return &trackedDNSConn{Conn: conn, proxy: p}, nil
	}
	return nil, lastError
}

func (p *dnsProxy) serveUDP() {
	defer p.workers.Done()
	buffer := make([]byte, maxDNSMessage)
	for {
		n, sender, err := p.udp.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		query := append([]byte(nil), buffer[:n]...)
		if validateDNSQuery(query) != nil {
			continue
		}
		select {
		case p.semaphore <- struct{}{}:
		default:
			p.udp.WriteToUDP(dnsErrorResponse(query, false), sender)
			continue
		}
		p.workers.Add(1)
		go func() {
			defer p.workers.Done()
			defer func() { <-p.semaphore }()
			response, err := p.forward(p.ctx, query)
			if err != nil {
				response = dnsErrorResponse(query, false)
			} else if len(response) > dnsUDPSize(query) {
				response = dnsErrorResponse(query, true)
			}
			p.udp.WriteToUDP(response, sender)
		}()
	}
}

func (p *dnsProxy) serveTCP() {
	defer p.workers.Done()
	for {
		conn, err := p.tcp.Accept()
		if err != nil {
			return
		}
		select {
		case p.semaphore <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		if !p.track(conn) {
			<-p.semaphore
			continue
		}
		p.workers.Add(1)
		go func() {
			defer p.workers.Done()
			defer func() { <-p.semaphore; p.untrack(conn); conn.Close() }()
			for {
				conn.SetDeadline(time.Now().Add(30 * time.Second))
				query, err := readDNSFrame(conn)
				if err != nil || validateDNSQuery(query) != nil {
					return
				}
				response, err := p.forward(p.ctx, query)
				if err != nil {
					response = dnsErrorResponse(query, false)
				}
				if writeDNSFrame(conn, response) != nil {
					return
				}
			}
		}()
	}
}

func (p *dnsProxy) forward(ctx context.Context, query []byte) ([]byte, error) {
	if err := validateDNSQuery(query); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	var response []byte
	var err error
	if p.config.Type == "doh" {
		response, err = p.forwardDoH(ctx, query)
	} else {
		response, err = p.forwardDoT(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	if err := validateDNSResponse(query, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (p *dnsProxy) forwardDoH(ctx context.Context, query []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.dohURL, bytes.NewReader(query))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept", "application/dns-message")
	response, err := p.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaType != "application/dns-message" {
		return nil, fmt.Errorf("некорректный ответ DoH: HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, maxDNSMessage+1))
}

func (p *dnsProxy) forwardDoT(ctx context.Context, query []byte) ([]byte, error) {
	// Retry a closed pooled connection once. DNS requests are idempotent.
	for attempt := 0; attempt < 2; attempt++ {
		var conn net.Conn
		pooled := false
		select {
		case conn = <-p.dotPool:
			pooled = true
		default:
		}
		if conn == nil {
			raw, err := p.dialContext(ctx, "tcp", net.JoinHostPort(p.host, p.port))
			if err != nil {
				return nil, err
			}
			tlsConn := tls.Client(raw, p.tlsConfig)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				raw.Close()
				return nil, err
			}
			conn = tlsConn
		}
		deadline, _ := ctx.Deadline()
		conn.SetDeadline(deadline)
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		err := writeDNSFrame(conn, query)
		var response []byte
		if err == nil {
			response, err = readDNSFrame(conn)
		}
		if err == nil {
			err = validateDNSResponse(query, response)
		}
		if !stop() || ctx.Err() != nil {
			conn.Close()
			return nil, context.Canceled
		}
		if err != nil {
			conn.Close()
			if pooled && attempt == 0 {
				continue
			}
			return nil, err
		}
		conn.SetDeadline(time.Time{})
		select {
		case p.dotPool <- conn:
		default:
			conn.Close()
		}
		return response, nil
	}
	return nil, errors.New("нет ответа DoT")
}

func readDNSFrame(reader io.Reader) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(header[:]))
	if length < 12 {
		return nil, errors.New("слишком короткий ответ DNS")
	}
	message := make([]byte, length)
	_, err := io.ReadFull(reader, message)
	return message, err
}

func writeDNSFrame(writer io.Writer, message []byte) error {
	if len(message) < 12 || len(message) > maxDNSMessage {
		return errors.New("некорректный размер DNS-пакета")
	}
	frame := make([]byte, len(message)+2)
	binary.BigEndian.PutUint16(frame[:2], uint16(len(message)))
	copy(frame[2:], message)
	_, err := io.Copy(writer, bytes.NewReader(frame))
	return err
}

func skipDNSName(message []byte, offset int) (int, error) {
	start := offset
	for offset < len(message) && offset-start <= 255 {
		length := int(message[offset])
		offset++
		if length == 0 {
			return offset, nil
		}
		if length&0xc0 == 0xc0 && offset < len(message) {
			pointer := (length&0x3f)<<8 | int(message[offset])
			if pointer >= 12 && pointer < len(message) {
				return offset + 1, nil
			}
			break
		}
		if length > 63 || offset+length >= len(message) {
			break
		}
		offset += length
	}
	return 0, errors.New("некорректное имя в DNS-пакете")
}

func dnsQuestionEnd(query []byte) (int, error) {
	if len(query) < 12 {
		return 0, errors.New("слишком короткий запрос DNS")
	}
	count := int(binary.BigEndian.Uint16(query[4:6]))
	if count == 0 || count > 64 {
		return 0, errors.New("некорректное число вопросов DNS")
	}
	offset := 12
	for i := 0; i < count; i++ {
		next, err := skipDNSName(query, offset)
		if err != nil || next+4 > len(query) {
			return 0, errors.New("некорректный вопрос DNS")
		}
		offset = next + 4
	}
	return offset, nil
}

func validateDNSQuery(query []byte) error {
	if len(query) < 12 || len(query) > maxDNSMessage || query[2]&0x80 != 0 || binary.BigEndian.Uint16(query[6:8]) != 0 || binary.BigEndian.Uint16(query[8:10]) != 0 {
		return errors.New("некорректный запрос DNS")
	}
	_, err := dnsQuestionEnd(query)
	return err
}

func validateDNSResponse(query, response []byte) error {
	if len(query) < 12 || len(response) < 12 || len(response) > maxDNSMessage || response[0] != query[0] || response[1] != query[1] || response[2]&0x80 == 0 || response[2]&0x78 != query[2]&0x78 {
		return errors.New("некорректный ответ DNS")
	}
	return nil
}

func dnsErrorResponse(query []byte, truncated bool) []byte {
	end, err := dnsQuestionEnd(query)
	if err != nil {
		return nil
	}
	response := append([]byte(nil), query[:end]...)
	flags := binary.BigEndian.Uint16(query[2:4])&0x7910 | 0x8082 // QR, RA, SERVFAIL; preserve opcode, RD, CD.
	if truncated {
		flags = (flags &^ 0xf) | 0x200
	}
	binary.BigEndian.PutUint16(response[2:4], flags)
	clear(response[6:12])
	return response
}

func dnsUDPSize(query []byte) int {
	offset, err := dnsQuestionEnd(query)
	if err != nil {
		return 512
	}
	for n := int(binary.BigEndian.Uint16(query[10:12])); n > 0; n-- {
		offset, err = skipDNSName(query, offset)
		if err != nil || offset+10 > len(query) {
			return 512
		}
		if binary.BigEndian.Uint16(query[offset:offset+2]) == 41 {
			return max(512, min(int(binary.BigEndian.Uint16(query[offset+2:offset+4])), 65507))
		}
		offset += 10 + int(binary.BigEndian.Uint16(query[offset+8:offset+10]))
		if offset > len(query) {
			return 512
		}
	}
	return 512
}

func verifyDNSServer(ctx context.Context, config dnsConfig) error {
	if !config.Enabled || config.Type == "udp" {
		return validateDNSConfig(config)
	}
	p, err := newDNSProxy(config)
	if err != nil {
		return err
	}
	defer p.Close()
	query := []byte{0, 0, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
	if _, err := rand.Read(query[:2]); err != nil {
		return err
	}
	response, err := p.forward(ctx, query)
	if err != nil {
		return fmt.Errorf("проверка DNS-сервера не пройдена: %w", err)
	}
	if response[3]&0xf != 0 || binary.BigEndian.Uint16(response[6:8]) == 0 {
		return errors.New("DNS-сервер не вернул адрес для проверочного запроса")
	}
	return nil
}
