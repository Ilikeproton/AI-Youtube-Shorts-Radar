package netutil

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPClientWithProxy(t *testing.T) {
	t.Parallel()

	client, err := NewHTTPClient("socks5://127.0.0.1:10625", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.DialContext == nil {
		t.Fatal("expected DialContext to be configured for socks5 proxy")
	}
	if transport.Proxy != nil {
		t.Fatal("expected HTTP proxy func to be disabled when socks5 dialer is used")
	}
}

func TestResolveProxyURLBareAddressPrefersSocks5(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 3)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		_, _ = conn.Write([]byte{0x05, 0x00})
	}()

	resolved, err := ResolveProxyURL(listener.Addr().String())
	if err != nil {
		t.Fatalf("ResolveProxyURL failed: %v", err)
	}
	if want := "socks5://" + listener.Addr().String(); resolved != want {
		t.Fatalf("expected %q, got %q", want, resolved)
	}
}

func TestResolveProxyURLBareAddressFallsBackToHTTP(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(conn, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
	}()

	resolved, err := ResolveProxyURL(listener.Addr().String())
	if err != nil {
		t.Fatalf("ResolveProxyURL failed: %v", err)
	}
	if want := "http://" + listener.Addr().String(); resolved != want {
		t.Fatalf("expected %q, got %q", want, resolved)
	}
}

func TestNewHTTPClientWithHTTPProxy(t *testing.T) {
	t.Parallel()

	client, err := NewHTTPClient("http://127.0.0.1:10601", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("expected HTTP proxy func to be configured")
	}
	if transport.DialContext != nil {
		t.Fatal("expected DialContext to remain default for http proxy")
	}
}
