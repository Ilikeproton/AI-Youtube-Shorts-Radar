package netutil

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

func NewHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        16,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	if proxyURL != "" {
		resolvedProxyURL, err := ResolveProxyURL(proxyURL)
		if err != nil {
			return nil, err
		}
		if resolvedProxyURL != "" {
			parsed, err := url.Parse(resolvedProxyURL)
			if err != nil {
				return nil, err
			}
			switch strings.ToLower(parsed.Scheme) {
			case "socks5", "socks5h":
				dialer, err := xproxy.FromURL(parsed, xproxy.Direct)
				if err != nil {
					return nil, err
				}
				if contextDialer, ok := dialer.(xproxy.ContextDialer); ok {
					transport.DialContext = contextDialer.DialContext
				} else {
					transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
						type dialResult struct {
							conn net.Conn
							err  error
						}
						done := make(chan dialResult, 1)
						go func() {
							conn, err := dialer.Dial(network, addr)
							done <- dialResult{conn: conn, err: err}
						}()
						select {
						case <-ctx.Done():
							return nil, ctx.Err()
						case result := <-done:
							return result.conn, result.err
						}
					}
				}
				transport.Proxy = nil
			case "http", "https":
				transport.Proxy = http.ProxyURL(parsed)
			default:
				return nil, fmt.Errorf("unsupported proxy scheme: %s", parsed.Scheme)
			}
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}, nil
}

func ResolveProxyURL(proxyURL string) (string, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return "", nil
	}
	if strings.Contains(proxyURL, "://") {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return "", err
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return "", fmt.Errorf("invalid proxy url: %s", proxyURL)
		}
		return proxyURL, nil
	}
	if !looksLikeHostPort(proxyURL) {
		return "", fmt.Errorf("invalid proxy address: %s", proxyURL)
	}
	if isLikelySocks5(proxyURL, 1200*time.Millisecond) {
		return "socks5://" + proxyURL, nil
	}
	return "http://" + proxyURL, nil
}

func looksLikeHostPort(value string) bool {
	parsed, err := url.Parse("//" + value)
	if err != nil {
		return false
	}
	return parsed.Host != "" && parsed.Hostname() != "" && parsed.Port() != ""
}

func isLikelySocks5(addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return false
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return false
	}
	return reply[0] == 0x05
}
