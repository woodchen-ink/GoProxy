package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
)

// TestDetectInboundProtocol 保证混合入口能按首字节区分 SOCKS5 与 HTTP。
func TestDetectInboundProtocol(t *testing.T) {
	testCases := []struct {
		name  string
		input []byte
		want  inboundProtocol
	}{
		{name: "socks5 greeting", input: []byte{0x05, 0x01, 0x00}, want: inboundProtocolSOCKS5},
		{name: "http connect", input: []byte("CONNECT example.com:443 HTTP/1.1\r\n"), want: inboundProtocolHTTP},
		{name: "http get", input: []byte("GET http://example.com/ HTTP/1.1\r\n"), want: inboundProtocolHTTP},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detectInboundProtocol(bufio.NewReader(bytes.NewReader(tc.input)))
			if err != nil {
				t.Fatalf("detectInboundProtocol: %v", err)
			}
			if got != tc.want {
				t.Fatalf("protocol = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBufferedConnPreservesPeekedBytes 保证协议探测后首包仍会被后续处理链读到。
func TestBufferedConnPreservesPeekedBytes(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	payload := []byte("CONNECT example.com:443 HTTP/1.1\r\n\r\n")
	writeDone := make(chan error, 1)
	go func() {
		_, err := clientConn.Write(payload)
		writeDone <- err
	}()

	reader := bufio.NewReader(serverConn)
	if _, err := detectInboundProtocol(reader); err != nil {
		t.Fatalf("detectInboundProtocol: %v", err)
	}

	wrappedConn := &bufferedConn{
		Conn:   serverConn,
		reader: reader,
	}

	got, err := io.ReadAll(io.LimitReader(wrappedConn, int64(len(payload))))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload = %q, want %q", string(got), string(payload))
	}

	if err := <-writeDone; err != nil {
		t.Fatalf("client write: %v", err)
	}
}
