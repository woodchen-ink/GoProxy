package proxy

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// TestResolveSOCKS5FallbackTargets 保证本地 DNS 兜底会保留端口并返回至少一个地址。
func TestResolveSOCKS5FallbackTargets(t *testing.T) {
	targets, err := resolveSOCKS5FallbackTargets("localhost:443", 2*time.Second)
	if err != nil {
		t.Fatalf("resolve fallback targets: %v", err)
	}

	if len(targets) == 0 {
		t.Fatal("expected at least one fallback target")
	}

	for _, target := range targets {
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			t.Fatalf("split host port for %q: %v", target, err)
		}
		if port != "443" {
			t.Fatalf("expected port 443, got %s", port)
		}
		if ip := net.ParseIP(host); ip == nil {
			t.Fatalf("expected ip host in %q", target)
		}
	}
}

// TestReadSOCKS5ConnectReplyConsumesFullIPv6Reply 保证读取完整回复后不会残留地址字节污染后续流量。
func TestReadSOCKS5ConnectReplyConsumesFullIPv6Reply(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	done := make(chan error, 1)
	go func() {
		reply := []byte{
			0x05, 0x00, 0x00, 0x04,
			0x20, 0x01, 0x0d, 0xb8, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
			0x01, 0xbb,
		}
		_, err := serverConn.Write(append(reply, []byte("ok")...))
		done <- err
	}()

	if err := readSOCKS5ConnectReply(clientConn); err != nil {
		t.Fatalf("read connect reply: %v", err)
	}

	buf := make([]byte, 2)
	if _, err := io.ReadFull(clientConn, buf); err != nil {
		t.Fatalf("read payload after reply: %v", err)
	}

	if string(buf) != "ok" {
		t.Fatalf("expected payload ok, got %q", string(buf))
	}

	if err := <-done; err != nil && !strings.Contains(err.Error(), "closed") {
		t.Fatalf("writer error: %v", err)
	}
}
