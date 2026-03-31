package proxy

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goproxy/config"
	"goproxy/storage"
)

// TestReadSOCKS5RequestDomainAcrossMultipleReads 保证域名请求即使被拆包也能完整解析。
func TestReadSOCKS5RequestDomainAcrossMultipleReads(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	done := make(chan error, 1)
	go func() {
		header := []byte{0x05, 0x01, 0x00, 0x03}
		payload := []byte{
			0x0b,
			'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm',
			0x01, 0xbb,
		}

		if _, err := clientConn.Write(header); err != nil {
			done <- err
			return
		}
		time.Sleep(10 * time.Millisecond)
		_, err := clientConn.Write(payload)
		done <- err
	}()

	server := &SOCKS5Server{}
	target, err := server.readSOCKS5Request(serverConn)
	if err != nil {
		t.Fatalf("readSOCKS5Request: %v", err)
	}

	if target != "example.com:443" {
		t.Fatalf("target = %q, want %q", target, "example.com:443")
	}

	if err := <-done; err != nil {
		t.Fatalf("writer error: %v", err)
	}
}

// TestSelectUpstreamProxyAllowsHTTPUpstream 保证开启开关后，下游 SOCKS5 可以复用 HTTP 上游。
func TestSelectUpstreamProxyAllowsHTTPUpstream(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "proxy.db")
	store, err := storage.New(dbPath)
	if err != nil {
		if strings.Contains(err.Error(), "requires cgo") {
			t.Skip("sqlite3 driver requires cgo in this test environment")
		}
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	if err := store.AddProxy("127.0.0.1:8080", "http"); err != nil {
		t.Fatalf("AddProxy http: %v", err)
	}

	server := &SOCKS5Server{
		storage: store,
		cfg: &config.Config{
			SOCKS5AllowHTTPUpstream: true,
		},
		mode:     "lowest-latency",
		affinity: newSessionAffinityManager(0),
	}

	upstream, err := server.selectUpstreamProxy(nil, "")
	if err != nil {
		t.Fatalf("selectUpstreamProxy: %v", err)
	}

	if upstream.Protocol != "http" {
		t.Fatalf("protocol = %q, want %q", upstream.Protocol, "http")
	}
}

// TestSelectUpstreamProxyRejectsHTTPWhenDisabled 保证默认配置下仍然只接受 SOCKS5 上游。
func TestSelectUpstreamProxyRejectsHTTPWhenDisabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "proxy.db")
	store, err := storage.New(dbPath)
	if err != nil {
		if strings.Contains(err.Error(), "requires cgo") {
			t.Skip("sqlite3 driver requires cgo in this test environment")
		}
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	if err := store.AddProxy("127.0.0.1:8080", "http"); err != nil {
		t.Fatalf("AddProxy http: %v", err)
	}

	server := &SOCKS5Server{
		storage: store,
		cfg: &config.Config{
			SOCKS5AllowHTTPUpstream: false,
		},
		mode:     "lowest-latency",
		affinity: newSessionAffinityManager(0),
	}

	if _, err := server.selectUpstreamProxy(nil, ""); err == nil {
		t.Fatal("expected no upstream when only HTTP proxies exist and HTTP upstream is disabled")
	}
}

func TestParseSessionUsername(t *testing.T) {
	tests := []struct {
		name     string
		baseUser string
		username string
		wantOK   bool
		wantID   string
	}{
		{name: "plain username", baseUser: "proxy", username: "proxy", wantOK: true, wantID: ""},
		{name: "session suffix", baseUser: "proxy", username: "proxy-session-abc123", wantOK: true, wantID: "abc123"},
		{name: "sid suffix", baseUser: "proxy", username: "proxy-sid-reg-01", wantOK: true, wantID: "reg-01"},
		{name: "invalid prefix", baseUser: "proxy", username: "proxyx-session-1", wantOK: false, wantID: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOK, gotID := parseSessionUsername(tt.baseUser, tt.username)
			if gotOK != tt.wantOK || gotID != tt.wantID {
				t.Fatalf("parseSessionUsername(%q, %q) = (%v, %q), want (%v, %q)", tt.baseUser, tt.username, gotOK, gotID, tt.wantOK, tt.wantID)
			}
		})
	}
}
