package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// dialSOCKS5Upstream 优先让上游代理解析域名，失败时可选回退到本地 DNS 解析后的 IP。
func dialSOCKS5Upstream(proxyAddress, target string, timeout time.Duration, allowLocalDNSFallback bool) (net.Conn, string, bool, error) {
	conn, err := dialSOCKS5UpstreamOnce(proxyAddress, target, timeout)
	if err == nil {
		return conn, target, false, nil
	}

	if !allowLocalDNSFallback || !socks5TargetNeedsDNSFallback(target) {
		return nil, "", false, err
	}

	fallbackTargets, resolveErr := resolveSOCKS5FallbackTargets(target, timeout)
	if resolveErr != nil {
		return nil, "", false, fmt.Errorf("%w; local dns fallback unavailable: %v", err, resolveErr)
	}

	var lastFallbackErr error
	for _, fallbackTarget := range fallbackTargets {
		conn, fallbackErr := dialSOCKS5UpstreamOnce(proxyAddress, fallbackTarget, timeout)
		if fallbackErr == nil {
			return conn, fallbackTarget, true, nil
		}
		lastFallbackErr = fallbackErr
	}

	if lastFallbackErr == nil {
		lastFallbackErr = fmt.Errorf("no fallback address available")
	}

	return nil, "", false, fmt.Errorf("%w; local dns fallback failed: %v", err, lastFallbackErr)
}

// socks5TargetNeedsDNSFallback 判断目标是否仍然需要本地 DNS 兜底。
func socks5TargetNeedsDNSFallback(target string) bool {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return false
	}
	return net.ParseIP(host) == nil
}

// dialSOCKS5UpstreamOnce 建立到单个上游 SOCKS5 的连接并完成一次 CONNECT。
func dialSOCKS5UpstreamOnce(proxyAddress, target string, timeout time.Duration) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	proxyConn, err := dialer.Dial("tcp", proxyAddress)
	if err != nil {
		return nil, err
	}

	if err := proxyConn.SetDeadline(time.Now().Add(timeout)); err != nil {
		proxyConn.Close()
		return nil, err
	}

	if err := performSOCKS5Handshake(proxyConn); err != nil {
		proxyConn.Close()
		return nil, err
	}

	req, err := buildSOCKS5ConnectRequest(target)
	if err != nil {
		proxyConn.Close()
		return nil, err
	}

	if _, err := proxyConn.Write(req); err != nil {
		proxyConn.Close()
		return nil, err
	}

	if err := readSOCKS5ConnectReply(proxyConn); err != nil {
		proxyConn.Close()
		return nil, err
	}

	if err := proxyConn.SetDeadline(time.Time{}); err != nil {
		proxyConn.Close()
		return nil, err
	}

	return proxyConn, nil
}

// performSOCKS5Handshake 与上游 SOCKS5 代理完成无认证握手。
func performSOCKS5Handshake(conn net.Conn) error {
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}

	handshake := make([]byte, 2)
	if _, err := io.ReadFull(conn, handshake); err != nil {
		return err
	}

	if handshake[0] != 0x05 || handshake[1] != 0x00 {
		return fmt.Errorf("socks5 handshake failed")
	}

	return nil
}

// buildSOCKS5ConnectRequest 根据目标地址生成 SOCKS5 CONNECT 请求。
func buildSOCKS5ConnectRequest(target string) ([]byte, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}

	req := []byte{0x05, 0x01, 0x00}

	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, 0x01)
			req = append(req, ip4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("domain too long for socks5: %s", host)
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, []byte(host)...)
	}

	portNum := uint16(0)
	if _, err := fmt.Sscanf(port, "%d", &portNum); err != nil {
		return nil, fmt.Errorf("invalid port %q: %w", port, err)
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, portNum)

	return append(req, portBytes...), nil
}

// readSOCKS5ConnectReply 读取完整的 SOCKS5 CONNECT 响应，避免残留字节污染后续流量。
func readSOCKS5ConnectReply(conn net.Conn) error {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return err
	}

	if header[0] != 0x05 {
		return fmt.Errorf("invalid socks5 reply version: %d", header[0])
	}

	if header[1] != 0x00 {
		return fmt.Errorf("socks5 connect failed, code: %d", header[1])
	}

	addrLen, err := socks5ReplyAddressLength(conn, header[3])
	if err != nil {
		return err
	}

	discardLen := addrLen + 2
	if discardLen == 0 {
		return nil
	}

	if _, err := io.CopyN(io.Discard, conn, int64(discardLen)); err != nil {
		return err
	}

	return nil
}

// socks5ReplyAddressLength 读取并返回 SOCKS5 响应里的 BND.ADDR 长度。
func socks5ReplyAddressLength(conn net.Conn, atyp byte) (int, error) {
	switch atyp {
	case 0x01:
		return net.IPv4len, nil
	case 0x04:
		return net.IPv6len, nil
	case 0x03:
		buf := make([]byte, 1)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return 0, err
		}
		return int(buf[0]), nil
	default:
		return 0, fmt.Errorf("unsupported socks5 reply address type: %d", atyp)
	}
}

// resolveSOCKS5FallbackTargets 将域名解析为 IP 列表，并优先返回 IPv4 结果。
func resolveSOCKS5FallbackTargets(target string, timeout time.Duration) ([]string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}

	if ip := net.ParseIP(host); ip != nil {
		return nil, fmt.Errorf("target already uses an ip address")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(ips))
	var ipv4Targets []string
	var ipv6Targets []string

	for _, ip := range ips {
		ipStr := ip.String()
		if _, ok := seen[ipStr]; ok {
			continue
		}
		seen[ipStr] = struct{}{}

		address := net.JoinHostPort(ipStr, port)
		if ip.To4() != nil {
			ipv4Targets = append(ipv4Targets, address)
			continue
		}
		ipv6Targets = append(ipv6Targets, address)
	}

	targets := append(ipv4Targets, ipv6Targets...)
	if len(targets) == 0 {
		return nil, fmt.Errorf("no ip records resolved for %s", host)
	}

	return targets, nil
}
