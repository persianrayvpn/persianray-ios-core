package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
)

// Upstream SOCKS5 for every platform and provider dial. Empty means dial
// directly. Loopback is never sent through the proxy, so a local SOCKS
// listener cannot loop into itself. Read at dial time, so a process can set
// it before the network space is built and every later socket follows it.
var upstreamSocks atomic.Value // string

func SetUpstreamSocks(addr string) {
	upstreamSocks.Store(addr)
}

func UpstreamSocks() string {
	v, _ := upstreamSocks.Load().(string)
	return v
}

// Direct DoH, not the proxy and not the OS stub. usque resolves names with
// 1.1.1.1 inside the Warp tunnel, which returns NXDOMAIN or times out for
// URnetwork platform names the device itself can resolve. A DNS error is
// returned as *net.DNSError so an unprovisioned family name still falls back.
var upstreamDohClient = &http.Client{
	Timeout: 6 * time.Second,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 4 * time.Second}).DialContext,
		TLSHandshakeTimeout:   4 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
		ForceAttemptHTTP2:     true,
	},
}

// dialViaUpstream dials addr through the process SOCKS proxy. used is false
// when no proxy is set or the address is loopback. Hostnames are resolved
// here and the proxy is given the IP, so the proxy does not look them up.
func dialViaUpstream(ctx context.Context, network, addr string) (net.Conn, bool, error) {
	socks := UpstreamSocks()
	if socks == "" {
		return nil, false, nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, true, err
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil, false, nil
	}
	dialAddr := addr
	if net.ParseIP(host) == nil {
		ip, err := resolveUpstreamHost(ctx, network, host)
		if err != nil {
			return nil, true, err
		}
		dialAddr = net.JoinHostPort(ip.String(), port)
	}
	dialer, err := proxy.SOCKS5("tcp", socks, nil, proxy.Direct)
	if err != nil {
		return nil, true, err
	}
	if ctxDialer, ok := dialer.(proxy.ContextDialer); ok {
		conn, err := ctxDialer.DialContext(ctx, network, dialAddr)
		return conn, true, err
	}
	conn, err := dialer.Dial(network, dialAddr)
	return conn, true, err
}

func resolveUpstreamHost(ctx context.Context, network, host string) (net.IP, error) {
	switch network {
	case "tcp6", "udp6":
		return dohLookup(ctx, host, "AAAA")
	case "tcp4", "udp4":
		return dohLookup(ctx, host, "A")
	default:
		ip, err := dohLookup(ctx, host, "A")
		if err == nil {
			return ip, nil
		}
		var dnsErr *net.DNSError
		if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
			return nil, err
		}
		return dohLookup(ctx, host, "AAAA")
	}
}

func dohLookup(ctx context.Context, host, qtype string) (net.IP, error) {
	var last error
	notFound := 0
	for _, server := range []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"} {
		ip, err := dohQuery(ctx, server, host, qtype)
		if err == nil {
			return ip, nil
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			notFound++
		}
		last = err
	}
	if notFound == 2 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	if last == nil {
		last = &net.DNSError{Err: "DoH resolution failed", Name: host, IsTemporary: true}
	}
	return nil, last
}

type dohJSON struct {
	Status int `json:"Status"`
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

func dohQuery(ctx context.Context, server, host, qtype string) (net.IP, error) {
	u, err := url.Parse(server)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("name", host)
	q.Set("type", qtype)
	u.RawQuery = q.Encode()
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/dns-json")
	resp, err := upstreamDohClient.Do(req)
	if err != nil {
		return nil, &net.DNSError{Err: err.Error(), Name: host, IsTemporary: true}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, &net.DNSError{Err: err.Error(), Name: host, IsTemporary: true}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &net.DNSError{Err: fmt.Sprintf("DoH HTTP %d", resp.StatusCode), Name: host, IsTemporary: true}
	}
	var parsed dohJSON
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, &net.DNSError{Err: err.Error(), Name: host, IsTemporary: true}
	}
	if parsed.Status == 3 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	want := 1
	if qtype == "AAAA" {
		want = 28
	}
	for _, ans := range parsed.Answer {
		if ans.Type != want {
			continue
		}
		if ip := net.ParseIP(ans.Data); ip != nil {
			return ip, nil
		}
	}
	if parsed.Status == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return nil, &net.DNSError{Err: "DoH resolution failed", Name: host, IsTemporary: true}
}
