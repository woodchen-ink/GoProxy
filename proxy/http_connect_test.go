package proxy

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestEstablishHTTPConnectTunnelSuccess(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrCh <- err
			return
		}
		defer conn.Close()

		reqBuf := make([]byte, 256)
		n, err := conn.Read(reqBuf)
		if err != nil {
			serverErrCh <- err
			return
		}
		request := string(reqBuf[:n])
		if !strings.Contains(request, "CONNECT example.com:443 HTTP/1.1\r\n") {
			serverErrCh <- io.ErrUnexpectedEOF
			return
		}
		if !strings.Contains(request, "Host: example.com:443\r\n") {
			serverErrCh <- io.ErrUnexpectedEOF
			return
		}

		_, err = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\nProxy-Agent: test\r\n\r\n"))
		serverErrCh <- err
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := establishHTTPConnectTunnel(conn, "example.com:443", 2*time.Second); err != nil {
		t.Fatalf("establishHTTPConnectTunnel: %v", err)
	}

	if err := <-serverErrCh; err != nil {
		t.Fatalf("server error: %v", err)
	}
}

func TestEstablishHTTPConnectTunnelRejectsNon200(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrCh <- err
			return
		}
		defer conn.Close()

		reqBuf := make([]byte, 256)
		if _, err := conn.Read(reqBuf); err != nil {
			serverErrCh <- err
			return
		}

		_, err = conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\n\r\n"))
		serverErrCh <- err
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	err = establishHTTPConnectTunnel(conn, "example.com:443", 2*time.Second)
	if err == nil {
		t.Fatal("expected CONNECT handshake to fail on non-200 response")
	}
	if !strings.Contains(err.Error(), "407") {
		t.Fatalf("expected 407 in error, got %v", err)
	}

	if err := <-serverErrCh; err != nil {
		t.Fatalf("server error: %v", err)
	}
}

func TestEstablishHTTPConnectTunnelTimesOutOnSilentProxy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrCh <- err
			return
		}
		defer conn.Close()

		reqBuf := make([]byte, 256)
		if _, err := conn.Read(reqBuf); err != nil {
			serverErrCh <- err
			return
		}

		time.Sleep(200 * time.Millisecond)
		serverErrCh <- nil
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	err = establishHTTPConnectTunnel(conn, "example.com:443", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected CONNECT handshake to time out on silent proxy")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}

	if err := <-serverErrCh; err != nil {
		t.Fatalf("server error: %v", err)
	}
}
