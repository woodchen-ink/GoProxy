package proxy

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goproxy/config"
	"goproxy/storage"
)

func newTestStorage(t *testing.T) *storage.Storage {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "proxy.db")
	store, err := storage.New(dbPath)
	if err != nil {
		if strings.Contains(err.Error(), "requires cgo") {
			t.Skip("sqlite3 driver requires cgo in this test environment")
		}
		t.Fatalf("storage.New: %v", err)
	}
	return store
}

func addTestProxy(t *testing.T, store *storage.Storage, address, protocol, exitIP string, latency int) {
	t.Helper()

	if err := store.AddProxy(address, protocol); err != nil {
		t.Fatalf("AddProxy(%s): %v", address, err)
	}
	if err := store.UpdateExitInfo(address, exitIP, "US Test", latency); err != nil {
		t.Fatalf("UpdateExitInfo(%s): %v", address, err)
	}
}

func TestSelectProxyAvoidsSharedExitIPAcrossSessions(t *testing.T) {
	store := newTestStorage(t)
	defer store.Close()

	addTestProxy(t, store, "10.0.0.1:8080", "http", "1.1.1.1", 10)
	addTestProxy(t, store, "10.0.0.2:8080", "http", "1.1.1.1", 20)
	addTestProxy(t, store, "10.0.0.3:8080", "http", "2.2.2.2", 30)

	server := &Server{
		storage:  store,
		cfg:      &config.Config{},
		mode:     "lowest-latency",
		affinity: newSessionAffinityManager(time.Minute),
	}

	first, err := server.selectProxy(nil, "session-a")
	if err != nil {
		t.Fatalf("selectProxy(session-a): %v", err)
	}
	if first.ExitIP != "1.1.1.1" {
		t.Fatalf("session-a exit_ip = %q, want %q", first.ExitIP, "1.1.1.1")
	}

	second, err := server.selectProxy(nil, "session-b")
	if err != nil {
		t.Fatalf("selectProxy(session-b): %v", err)
	}
	if second.ExitIP != "2.2.2.2" {
		t.Fatalf("session-b exit_ip = %q, want %q", second.ExitIP, "2.2.2.2")
	}
	if second.Address != "10.0.0.3:8080" {
		t.Fatalf("session-b address = %q, want %q", second.Address, "10.0.0.3:8080")
	}

	reused, err := server.selectProxy(nil, "session-a")
	if err != nil {
		t.Fatalf("selectProxy(session-a reuse): %v", err)
	}
	if reused.Address != first.Address {
		t.Fatalf("session-a reused address = %q, want %q", reused.Address, first.Address)
	}
}

func TestSelectProxyFallsBackWhenOnlySharedExitIPRemains(t *testing.T) {
	store := newTestStorage(t)
	defer store.Close()

	addTestProxy(t, store, "10.0.1.1:8080", "http", "3.3.3.3", 10)
	addTestProxy(t, store, "10.0.1.2:8080", "http", "3.3.3.3", 20)

	server := &Server{
		storage:  store,
		cfg:      &config.Config{},
		mode:     "lowest-latency",
		affinity: newSessionAffinityManager(time.Minute),
	}

	first, err := server.selectProxy(nil, "session-a")
	if err != nil {
		t.Fatalf("selectProxy(session-a): %v", err)
	}

	second, err := server.selectProxy(nil, "session-b")
	if err != nil {
		t.Fatalf("selectProxy(session-b): %v", err)
	}
	if second.ExitIP != first.ExitIP {
		t.Fatalf("session-b exit_ip = %q, want shared %q when pool is exhausted", second.ExitIP, first.ExitIP)
	}
}

func TestSelectProxyReleasesExitIPAfterAffinityTTL(t *testing.T) {
	store := newTestStorage(t)
	defer store.Close()

	addTestProxy(t, store, "10.0.2.1:8080", "http", "4.4.4.4", 10)
	addTestProxy(t, store, "10.0.2.2:8080", "http", "5.5.5.5", 20)

	server := &Server{
		storage:  store,
		cfg:      &config.Config{},
		mode:     "lowest-latency",
		affinity: newSessionAffinityManager(20 * time.Millisecond),
	}

	first, err := server.selectProxy(nil, "session-a")
	if err != nil {
		t.Fatalf("selectProxy(session-a): %v", err)
	}
	if first.Address != "10.0.2.1:8080" {
		t.Fatalf("session-a address = %q, want %q", first.Address, "10.0.2.1:8080")
	}

	time.Sleep(40 * time.Millisecond)

	second, err := server.selectProxy(nil, "session-b")
	if err != nil {
		t.Fatalf("selectProxy(session-b): %v", err)
	}
	if second.Address != "10.0.2.1:8080" {
		t.Fatalf("session-b address = %q, want %q after ttl expiry", second.Address, "10.0.2.1:8080")
	}
}

func TestSelectUpstreamProxyAvoidsSharedExitIPAcrossSessions(t *testing.T) {
	store := newTestStorage(t)
	defer store.Close()

	addTestProxy(t, store, "10.0.3.1:1080", "socks5", "6.6.6.6", 10)
	addTestProxy(t, store, "10.0.3.2:1080", "socks5", "6.6.6.6", 20)
	addTestProxy(t, store, "10.0.3.3:1080", "socks5", "7.7.7.7", 30)

	server := &SOCKS5Server{
		storage: store,
		cfg: &config.Config{
			SOCKS5AllowHTTPUpstream: false,
		},
		mode:     "lowest-latency",
		affinity: newSessionAffinityManager(time.Minute),
	}

	first, err := server.selectUpstreamProxy(nil, "session-a")
	if err != nil {
		t.Fatalf("selectUpstreamProxy(session-a): %v", err)
	}
	if first.ExitIP != "6.6.6.6" {
		t.Fatalf("session-a exit_ip = %q, want %q", first.ExitIP, "6.6.6.6")
	}

	second, err := server.selectUpstreamProxy(nil, "session-b")
	if err != nil {
		t.Fatalf("selectUpstreamProxy(session-b): %v", err)
	}
	if second.ExitIP != "7.7.7.7" {
		t.Fatalf("session-b exit_ip = %q, want %q", second.ExitIP, "7.7.7.7")
	}
}
