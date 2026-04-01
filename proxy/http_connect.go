package proxy

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// establishHTTPConnectTunnel 向上游 HTTP 代理发起 CONNECT，并要求收到明确的 200 响应。
func establishHTTPConnectTunnel(conn net.Conn, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set CONNECT deadline: %w", err)
	}

	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		return fmt.Errorf("write CONNECT request: %w", err)
	}

	statusCode, statusLine, err := readHTTPConnectResponse(conn)
	if err != nil {
		return fmt.Errorf("read CONNECT response: %w", err)
	}
	if statusCode != 200 {
		return fmt.Errorf("upstream proxy CONNECT failed: %s", statusLine)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear CONNECT deadline: %w", err)
	}
	return nil
}

func readHTTPConnectResponse(conn net.Conn) (int, string, error) {
	reader := bufio.NewReader(conn)

	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	statusLine = strings.TrimSpace(statusLine)

	parts := strings.SplitN(statusLine, " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return 0, statusLine, fmt.Errorf("malformed CONNECT response: %q", statusLine)
	}

	statusCode, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, statusLine, fmt.Errorf("invalid CONNECT status code in %q", statusLine)
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, statusLine, err
		}
		if line == "\r\n" {
			break
		}
	}

	return statusCode, statusLine, nil
}
