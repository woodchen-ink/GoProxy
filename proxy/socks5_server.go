package proxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"goproxy/config"
	"goproxy/storage"
)

// SOCKS5Server SOCKS5 协议服务器
type SOCKS5Server struct {
	storage *storage.Storage
	cfg     *config.Config
	mode    string // "random" 或 "lowest-latency"
	port    string
}

// NewSOCKS5 创建 SOCKS5 服务器
func NewSOCKS5(s *storage.Storage, cfg *config.Config, mode string, port string) *SOCKS5Server {
	return &SOCKS5Server{
		storage: s,
		cfg:     cfg,
		mode:    mode,
		port:    port,
	}
}

// Start 启动 SOCKS5 服务器
func (s *SOCKS5Server) Start() error {
	modeDesc := "随机轮换"
	if s.mode == "lowest-latency" {
		modeDesc = "最低延迟"
	}
	authStatus := "无认证"
	if s.cfg.ProxyAuthEnabled {
		authStatus = fmt.Sprintf("需认证 (用户: %s)", s.cfg.ProxyAuthUsername)
	}
	log.Printf("socks5 server listening on %s [%s] [%s]", s.port, modeDesc, authStatus)

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
	if err := s.socks5Handshake(clientConn); err != nil {
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
	// 重试机制：只使用 SOCKS5 协议的上游代理（天然支持 HTTPS）
	tried := []string{}
	maxRetries := s.cfg.MaxRetry + 2 // 增加重试次数以应对质量差的代理

	for attempt := 0; attempt <= maxRetries; attempt++ {
		var p *storage.Proxy
		var err error

		// SOCKS5 服务只使用 SOCKS5 上游代理
		if s.mode == "lowest-latency" {
			p, err = s.storage.GetLowestLatencyByProtocolExclude("socks5", tried)
		} else {
			p, err = s.storage.GetRandomByProtocolExclude("socks5", tried)
		}

		if err != nil {
			log.Printf("[socks5] no available socks5 upstream proxy: %v", err)
			s.sendSOCKS5Reply(clientConn, 0x01) // General failure
			return
		}

		tried = append(tried, p.Address)

		// 连接上游代理
		upstreamConn, connectedTarget, usedLocalDNSFallback, err := s.dialViaProxy(p, target)
		if err != nil {
			log.Printf("[socks5] dial %s via %s (%s) failed: %v, removing", target, p.Address, p.Protocol, err)
			s.storage.Delete(p.Address)
			continue
		}

		// 发送成功响应
		if err := s.sendSOCKS5Reply(clientConn, 0x00); err != nil {
			upstreamConn.Close()
			return
		}

		if usedLocalDNSFallback {
			log.Printf("[socks5] %s via %s established (local dns fallback -> %s)", target, p.Address, connectedTarget)
		} else {
			log.Printf("[socks5] %s via %s established", target, p.Address)
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
	log.Printf("[socks5] all proxies failed for %s", target)
}

// socks5Handshake 处理 SOCKS5 握手
func (s *SOCKS5Server) socks5Handshake(conn net.Conn) error {
	buf := make([]byte, 257)

	// 读取客户端问候: [VER(1), NMETHODS(1), METHODS(1-255)]
	n, err := io.ReadAtLeast(conn, buf, 2)
	if err != nil {
		return err
	}

	version := buf[0]
	if version != 0x05 {
		return fmt.Errorf("unsupported SOCKS version: %d", version)
	}

	nmethods := int(buf[1])
	if n < 2+nmethods {
		if _, err := io.ReadFull(conn, buf[n:2+nmethods]); err != nil {
			return err
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
		return err
	}

	if selectedMethod == 0xFF {
		return fmt.Errorf("no acceptable authentication method")
	}

	// 如果需要认证，进行用户名/密码认证
	if selectedMethod == 0x02 {
		if err := s.socks5Auth(conn); err != nil {
			return err
		}
	}

	return nil
}

// socks5Auth 处理 SOCKS5 用户名/密码认证
func (s *SOCKS5Server) socks5Auth(conn net.Conn) error {
	buf := make([]byte, 513)

	// 读取认证请求: [VER(1), ULEN(1), UNAME(1-255), PLEN(1), PASSWD(1-255)]
	n, err := io.ReadAtLeast(conn, buf, 2)
	if err != nil {
		return err
	}

	if buf[0] != 0x01 {
		return fmt.Errorf("unsupported auth version: %d", buf[0])
	}

	ulen := int(buf[1])
	if n < 2+ulen {
		if _, err := io.ReadFull(conn, buf[n:2+ulen]); err != nil {
			return err
		}
		n = 2 + ulen
	}

	username := string(buf[2 : 2+ulen])

	// 读取密码长度和密码
	if n < 2+ulen+1 {
		if _, err := io.ReadFull(conn, buf[n:2+ulen+1]); err != nil {
			return err
		}
		n = 2 + ulen + 1
	}

	plen := int(buf[2+ulen])
	if n < 2+ulen+1+plen {
		if _, err := io.ReadFull(conn, buf[n:2+ulen+1+plen]); err != nil {
			return err
		}
	}

	password := string(buf[2+ulen+1 : 2+ulen+1+plen])

	// 验证用户名和密码
	if username != s.cfg.ProxyAuthUsername || password != s.cfg.ProxyAuthPassword {
		// 认证失败: [VER(1), STATUS(1)]
		conn.Write([]byte{0x01, 0x01})
		return fmt.Errorf("authentication failed")
	}

	// 认证成功: [VER(1), STATUS(1)]
	if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
		return err
	}

	return nil
}

// readSOCKS5Request 读取 SOCKS5 请求
func (s *SOCKS5Server) readSOCKS5Request(conn net.Conn) (string, error) {
	buf := make([]byte, 262)

	// 读取请求: [VER(1), CMD(1), RSV(1), ATYP(1), DST.ADDR(variable), DST.PORT(2)]
	n, err := io.ReadAtLeast(conn, buf, 4)
	if err != nil {
		return "", err
	}

	if buf[0] != 0x05 {
		return "", fmt.Errorf("invalid version: %d", buf[0])
	}

	cmd := buf[1]
	if cmd != 0x01 { // 只支持 CONNECT
		s.sendSOCKS5Reply(conn, 0x07) // Command not supported
		return "", fmt.Errorf("unsupported command: %d", cmd)
	}

	atyp := buf[3]
	var host string
	var addrLen int

	switch atyp {
	case 0x01: // IPv4
		addrLen = 4
		if n < 4+addrLen+2 {
			if _, err := io.ReadFull(conn, buf[n:4+addrLen+2]); err != nil {
				return "", err
			}
		}
		host = fmt.Sprintf("%d.%d.%d.%d", buf[4], buf[5], buf[6], buf[7])
	case 0x03: // Domain name
		addrLen = int(buf[4])
		if n < 4+1+addrLen+2 {
			if _, err := io.ReadFull(conn, buf[n:4+1+addrLen+2]); err != nil {
				return "", err
			}
		}
		host = string(buf[5 : 5+addrLen])
	case 0x04: // IPv6
		addrLen = 16
		if n < 4+addrLen+2 {
			if _, err := io.ReadFull(conn, buf[n:4+addrLen+2]); err != nil {
				return "", err
			}
		}
		// 简化处理，直接转换
		host = net.IP(buf[4 : 4+addrLen]).String()
	default:
		s.sendSOCKS5Reply(conn, 0x08) // Address type not supported
		return "", fmt.Errorf("unsupported address type: %d", atyp)
	}

	// 读取端口
	portOffset := 4
	if atyp == 0x03 {
		portOffset = 5 + addrLen
	} else {
		portOffset = 4 + addrLen
	}
	port := binary.BigEndian.Uint16(buf[portOffset : portOffset+2])

	return fmt.Sprintf("%s:%d", host, port), nil
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

// dialViaProxy 通过上游代理连接目标，并在需要时启用本地 DNS 兜底。
func (s *SOCKS5Server) dialViaProxy(p *storage.Proxy, target string) (net.Conn, string, bool, error) {
	timeout := time.Duration(s.cfg.ValidateTimeout) * time.Second

	switch p.Protocol {
	case "http":
		// 连接到 HTTP 代理
		conn, err := net.DialTimeout("tcp", p.Address, timeout)
		if err != nil {
			return nil, "", false, err
		}
		// 发送 CONNECT 请求
		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		buf := make([]byte, 256)
		n, err := conn.Read(buf)
		if err != nil {
			conn.Close()
			return nil, "", false, err
		}
		// 检查 HTTP 响应
		if n < 12 || string(buf[:12]) != "HTTP/1.1 200" {
			conn.Close()
			return nil, "", false, fmt.Errorf("upstream proxy connect failed")
		}
		return conn, target, false, nil

	case "socks5":
		conn, connectedTarget, usedLocalDNSFallback, err := dialSOCKS5Upstream(
			p.Address,
			target,
			timeout,
			s.cfg.SOCKS5LocalDNSFallback,
		)
		if err != nil {
			return nil, "", false, err
		}
		return conn, connectedTarget, usedLocalDNSFallback, nil

	default:
		return nil, "", false, fmt.Errorf("unsupported protocol: %s", p.Protocol)
	}
}
