package proxy

import (
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"goproxy/config"
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

// TestDialSOCKS5UpstreamLocalModeUsesResolvedIP 保证 local 模式发给上游的是 IP 而不是域名。
func TestDialSOCKS5UpstreamLocalModeUsesResolvedIP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	atypCh := make(chan byte, 1)
	targetCh := make(chan string, 1)
	serverErrCh := make(chan error, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrCh <- err
			return
		}
		defer conn.Close()

		greeting := make([]byte, 3)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			serverErrCh <- err
			return
		}

		if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
			serverErrCh <- err
			return
		}

		header := make([]byte, 4)
		if _, err := io.ReadFull(conn, header); err != nil {
			serverErrCh <- err
			return
		}

		atyp := header[3]
		target, err := readTestSOCKS5Target(conn, atyp)
		if err != nil {
			serverErrCh <- err
			return
		}

		atypCh <- atyp
		targetCh <- target

		if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
			serverErrCh <- err
			return
		}

		serverErrCh <- nil
	}()

	conn, connectedTarget, usedLocalDNS, err := dialSOCKS5Upstream(
		listener.Addr().String(),
		"localhost:443",
		2*time.Second,
		config.SOCKS5DNSModeLocal,
	)
	if err != nil {
		t.Fatalf("dialSOCKS5Upstream: %v", err)
	}
	conn.Close()

	if !usedLocalDNS {
		t.Fatal("expected local DNS to be used in local mode")
	}

	host, _, err := net.SplitHostPort(connectedTarget)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", connectedTarget, err)
	}
	if net.ParseIP(host) == nil {
		t.Fatalf("expected resolved IP target, got %q", connectedTarget)
	}

	atyp := <-atypCh
	if atyp == 0x03 {
		t.Fatalf("expected upstream CONNECT to use IP address, got domain atyp %d", atyp)
	}

	serverTarget := <-targetCh
	serverHost, _, err := net.SplitHostPort(serverTarget)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", serverTarget, err)
	}
	if net.ParseIP(serverHost) == nil {
		t.Fatalf("expected upstream server to receive IP target, got %q", serverTarget)
	}

	if err := <-serverErrCh; err != nil {
		t.Fatalf("server error: %v", err)
	}
}

// readTestSOCKS5Target 读取测试服务器收到的 SOCKS5 CONNECT 目标。
func readTestSOCKS5Target(conn net.Conn, atyp byte) (string, error) {
	var host string

	switch atyp {
	case 0x01:
		buf := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case 0x04:
		buf := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case 0x03:
		size := make([]byte, 1)
		if _, err := io.ReadFull(conn, size); err != nil {
			return "", err
		}
		buf := make([]byte, int(size[0]))
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		host = string(buf)
	default:
		return "", net.InvalidAddrError("unsupported atyp")
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return "", err
	}

	port := int(portBuf[0])<<8 | int(portBuf[1])
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}
