package proxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"goproxy/config"
	"goproxy/storage"
)

// SOCKS5Server SOCKS5 协议服务器
type SOCKS5Server struct {
	storage  *storage.Storage
	cfg      *config.Config
	mode     string // "random" 或 "lowest-latency"
	port     string
	affinity *sessionAffinityManager
}

// NewSOCKS5 创建 SOCKS5 服务器
func NewSOCKS5(s *storage.Storage, cfg *config.Config, mode string, port string) *SOCKS5Server {
	return &SOCKS5Server{
		storage:  s,
		cfg:      cfg,
		mode:     mode,
		port:     port,
		affinity: newSessionAffinityManager(0),
	}
}

// Start 启动 SOCKS5 服务器
func (s *SOCKS5Server) Start() error {
	log.Printf("socks5 server listening on %s [%s] [%s]",
		s.port, describeProxyMode(s.mode), describeProxyAuth(s.cfg))

	listener, err := net.Listen("tcp", s.port)
	if err != nil {
		return err
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go s.handleConnection(conn)
	}
}

// handleConnection 处理 SOCKS5 连接
func (s *SOCKS5Server) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	// SOCKS5 握手
	sessionID, err := s.socks5Handshake(clientConn)
	if err != nil {
		log.Printf("[socks5] handshake failed: %v", err)
		return
	}

	// 读取请求
	target, err := s.readSOCKS5Request(clientConn)
	if err != nil {
		log.Printf("[socks5] read request failed: %v", err)
		return
	}

	// 带重试的连接上游代理
	// 重试机制：默认只使用 SOCKS5 上游；如配置允许，也可回退到 HTTP CONNECT 上游
	tried := []string{}
	maxRetries := s.cfg.MaxRetry + 2 // 增加重试次数以应对质量差的代理

	for attempt := 0; attempt <= maxRetries; attempt++ {
		var p *storage.Proxy
		var err error

		p, err = s.selectUpstreamProxy(tried, sessionID)

		if err != nil {
			log.Printf("[socks5] no available upstream proxy: %v", err)
			s.sendSOCKS5Reply(clientConn, 0x01) // General failure
			return
		}

		tried = append(tried, p.Address)

		// 连接上游代理
		upstreamConn, connectedTarget, usedLocalDNS, err := s.dialViaProxy(p, target)
		if err != nil {
			log.Printf("[socks5] dial %s via %s (%s) failed: %v, removing (session=%s)", target, p.Address, p.Protocol, err, sessionID)
			s.storage.Delete(p.Address)
			continue
		}

		// 发送成功响应
		if err := s.sendSOCKS5Reply(clientConn, 0x00); err != nil {
			upstreamConn.Close()
			return
		}

		if usedLocalDNS {
			log.Printf("[socks5] %s via %s established (local dns -> %s, session=%s)", target, p.Address, connectedTarget, sessionID)
		} else {
			log.Printf("[socks5] %s via %s established (session=%s)", target, p.Address, sessionID)
		}

		// 双向转发数据
		go io.Copy(upstreamConn, clientConn)
		io.Copy(clientConn, upstreamConn)

		// 转发完成，关闭连接
		upstreamConn.Close()
		return
	}

	// 所有重试都失败
	s.sendSOCKS5Reply(clientConn, 0x01) // General failure
	log.Printf("[socks5] all proxies failed for %s (session=%s)", target, sessionID)
}

// selectUpstreamProxy 根据配置选择 SOCKS5 服务可用的上游代理。
func (s *SOCKS5Server) selectUpstreamProxy(excludes []string, sessionID string) (*storage.Proxy, error) {
	return s.affinity.resolve(
		sessionID,
		excludes,
		func(address string) (*storage.Proxy, error) {
			proxyItem, err := s.storage.GetByAddress(address)
			if err != nil {
				return nil, err
			}
			if !s.cfg.SOCKS5AllowHTTPUpstream && proxyItem.Protocol != "socks5" {
				return nil, fmt.Errorf("sticky proxy protocol no longer allowed: %s", proxyItem.Protocol)
			}
			return proxyItem, nil
		},
		func(excluded []string, exitIPExcludes []string) (*storage.Proxy, error) {
			if s.cfg.SOCKS5AllowHTTPUpstream {
				if s.mode == "lowest-latency" {
					if len(exitIPExcludes) > 0 {
						return s.storage.GetLowestLatencyExcludeExitIPs(excluded, exitIPExcludes)
					}
					return s.storage.GetLowestLatencyExclude(excluded)
				}
				if len(exitIPExcludes) > 0 {
					return s.storage.GetRandomExcludeExitIPs(excluded, exitIPExcludes)
				}
				return s.storage.GetRandomExclude(excluded)
			}

			if s.mode == "lowest-latency" {
				if len(exitIPExcludes) > 0 {
					return s.storage.GetLowestLatencyByProtocolExcludeExitIPs("socks5", excluded, exitIPExcludes)
				}
				return s.storage.GetLowestLatencyByProtocolExclude("socks5", excluded)
			}
			if len(exitIPExcludes) > 0 {
				return s.storage.GetRandomByProtocolExcludeExitIPs("socks5", excluded, exitIPExcludes)
			}
			return s.storage.GetRandomByProtocolExclude("socks5", excluded)
		},
	)
}

// socks5Handshake 处理 SOCKS5 握手
func (s *SOCKS5Server) socks5Handshake(conn net.Conn) (string, error) {
	buf := make([]byte, 257)

	// 读取客户端问候: [VER(1), NMETHODS(1), METHODS(1-255)]
	n, err := io.ReadAtLeast(conn, buf, 2)
	if err != nil {
		return "", err
	}

	version := buf[0]
	if version != 0x05 {
		return "", fmt.Errorf("unsupported SOCKS version: %d", version)
	}

	nmethods := int(buf[1])
	if n < 2+nmethods {
		if _, err := io.ReadFull(conn, buf[n:2+nmethods]); err != nil {
			return "", err
		}
	}

	// 检查是否需要认证
	needAuth := s.cfg.ProxyAuthEnabled
	methods := buf[2 : 2+nmethods]

	// 选择认证方式
	var selectedMethod byte = 0xFF // No acceptable methods
	if needAuth {
		// 需要用户名/密码认证 (0x02)
		for _, method := range methods {
			if method == 0x02 {
				selectedMethod = 0x02
				break
			}
		}
	} else {
		// 无需认证 (0x00)
		for _, method := range methods {
			if method == 0x00 {
				selectedMethod = 0x00
				break
			}
		}
	}

	// 发送方法选择: [VER(1), METHOD(1)]
	if _, err := conn.Write([]byte{0x05, selectedMethod}); err != nil {
		return "", err
	}

	if selectedMethod == 0xFF {
		return "", fmt.Errorf("no acceptable authentication method")
	}

	// 如果需要认证，进行用户名/密码认证
	sessionID := ""
	if selectedMethod == 0x02 {
		parsedSessionID, err := s.socks5Auth(conn)
		if err != nil {
			return "", err
		}
		sessionID = parsedSessionID
	}

	return sessionID, nil
}

// socks5Auth 处理 SOCKS5 用户名/密码认证
func (s *SOCKS5Server) socks5Auth(conn net.Conn) (string, error) {
	buf := make([]byte, 513)

	// 读取认证请求: [VER(1), ULEN(1), UNAME(1-255), PLEN(1), PASSWD(1-255)]
	n, err := io.ReadAtLeast(conn, buf, 2)
	if err != nil {
		return "", err
	}

	if buf[0] != 0x01 {
		return "", fmt.Errorf("unsupported auth version: %d", buf[0])
	}

	ulen := int(buf[1])
	if n < 2+ulen {
		if _, err := io.ReadFull(conn, buf[n:2+ulen]); err != nil {
			return "", err
		}
		n = 2 + ulen
	}

	username := string(buf[2 : 2+ulen])

	// 读取密码长度和密码
	if n < 2+ulen+1 {
		if _, err := io.ReadFull(conn, buf[n:2+ulen+1]); err != nil {
			return "", err
		}
		n = 2 + ulen + 1
	}

	plen := int(buf[2+ulen])
	if n < 2+ulen+1+plen {
		if _, err := io.ReadFull(conn, buf[n:2+ulen+1+plen]); err != nil {
			return "", err
		}
	}

	password := string(buf[2+ulen+1 : 2+ulen+1+plen])
	authMatched, sessionID := parseSessionUsername(s.cfg.ProxyAuthUsername, username)

	// 验证用户名和密码
	if !authMatched || password != s.cfg.ProxyAuthPassword {
		// 认证失败: [VER(1), STATUS(1)]
		conn.Write([]byte{0x01, 0x01})
		return "", fmt.Errorf("authentication failed")
	}

	// 认证成功: [VER(1), STATUS(1)]
	if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
		return "", err
	}

	return sessionID, nil
}

// readSOCKS5Request 读取 SOCKS5 请求
func (s *SOCKS5Server) readSOCKS5Request(conn net.Conn) (string, error) {
	header := make([]byte, 4)

	// 读取请求头: [VER(1), CMD(1), RSV(1), ATYP(1)]
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", err
	}

	if header[0] != 0x05 {
		return "", fmt.Errorf("invalid version: %d", header[0])
	}

	cmd := header[1]
	if cmd != 0x01 { // 只支持 CONNECT
		s.sendSOCKS5Reply(conn, 0x07) // Command not supported
		return "", fmt.Errorf("unsupported command: %d", cmd)
	}

	host, err := s.readSOCKS5RequestHost(conn, header[3])
	if err != nil {
		if isSOCKS5AddressTypeError(err) {
			s.sendSOCKS5Reply(conn, 0x08) // Address type not supported
		}
		return "", err
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)

	return fmt.Sprintf("%s:%d", host, port), nil
}

// readSOCKS5RequestHost 按地址类型读取目标地址，避免可变长度域名被截断。
func (s *SOCKS5Server) readSOCKS5RequestHost(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		buf := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case 0x03:
		lengthBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lengthBuf); err != nil {
			return "", err
		}
		addrLen := int(lengthBuf[0])
		if addrLen == 0 {
			return "", fmt.Errorf("empty domain name in socks5 request")
		}
		buf := make([]byte, addrLen)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	case 0x04:
		buf := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	default:
		return "", fmt.Errorf("unsupported address type: %d", atyp)
	}
}

// isSOCKS5AddressTypeError 判断错误是否为地址类型不支持。
func isSOCKS5AddressTypeError(err error) bool {
	return strings.HasPrefix(err.Error(), "unsupported address type:")
}

// sendSOCKS5Reply 发送 SOCKS5 响应
func (s *SOCKS5Server) sendSOCKS5Reply(conn net.Conn, rep byte) error {
	// [VER(1), REP(1), RSV(1), ATYP(1), BND.ADDR(variable), BND.PORT(2)]
	// 简化：使用 0.0.0.0:0
	reply := []byte{
		0x05,       // VER
		rep,        // REP: 0x00=成功, 0x01=一般失败, 0x07=命令不支持, 0x08=地址类型不支持
		0x00,       // RSV
		0x01,       // ATYP: IPv4
		0, 0, 0, 0, // BND.ADDR: 0.0.0.0
		0, 0, // BND.PORT: 0
	}
	_, err := conn.Write(reply)
	return err
}

// dialViaProxy 通过上游代理连接目标，并按 DNS 模式决定是否本地解析。
func (s *SOCKS5Server) dialViaProxy(p *storage.Proxy, target string) (net.Conn, string, bool, error) {
	timeout := time.Duration(s.cfg.ValidateTimeout) * time.Second

	switch p.Protocol {
	case "http":
		// 连接到 HTTP 代理
		conn, err := net.DialTimeout("tcp", p.Address, timeout)
		if err != nil {
			return nil, "", false, err
		}
		if err := establishHTTPConnectTunnel(conn, target, timeout); err != nil {
			conn.Close()
			return nil, "", false, err
		}
		return conn, target, false, nil

	case "socks5":
		conn, connectedTarget, usedLocalDNSFallback, err := dialSOCKS5Upstream(
			p.Address,
			target,
			timeout,
			s.cfg.SOCKS5DNSMode,
		)
		if err != nil {
			return nil, "", false, err
		}
		return conn, connectedTarget, usedLocalDNSFallback, nil

	default:
		return nil, "", false, fmt.Errorf("unsupported protocol: %s", p.Protocol)
	}
}
