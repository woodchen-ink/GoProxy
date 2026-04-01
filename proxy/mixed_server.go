package proxy

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"sync"
)

type inboundProtocol string

const (
	inboundProtocolHTTP   inboundProtocol = "http"
	inboundProtocolSOCKS5 inboundProtocol = "socks5"
)

// MixedServer 在同一个 TCP 端口上同时接收 HTTP 代理和 SOCKS5 代理请求。
type MixedServer struct {
	httpServer   *Server
	socks5Server *SOCKS5Server
	port         string
}

// NewMixed 创建同端口双协议代理入口。
func NewMixed(httpServer *Server, socks5Server *SOCKS5Server, port string) *MixedServer {
	return &MixedServer{
		httpServer:   httpServer,
		socks5Server: socks5Server,
		port:         port,
	}
}

// Start 启动混合代理入口，并按首包自动分流到 HTTP 或 SOCKS5 处理链路。
func (s *MixedServer) Start() error {
	log.Printf("mixed proxy server listening on %s [%s] [HTTP+SOCKS5] [%s]",
		s.port, describeProxyMode(s.httpServer.mode), describeProxyAuth(s.httpServer.cfg))

	listener, err := net.Listen("tcp", s.port)
	if err != nil {
		return err
	}
	defer listener.Close()

	httpListener := newConnChanListener(listener.Addr())
	defer httpListener.Close()

	httpErrCh := make(chan error, 1)
	go func() {
		httpErrCh <- s.httpServer.Serve(httpListener)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case httpErr := <-httpErrCh:
				if httpErr != nil {
					return fmt.Errorf("serve mixed http: %w", httpErr)
				}
			default:
			}

			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				log.Printf("[mixed] temporary accept error on %s: %v", s.port, err)
				continue
			}
			return err
		}

		go s.routeConn(conn, httpListener)
	}
}

func (s *MixedServer) routeConn(conn net.Conn, httpListener *connChanListener) {
	reader := bufio.NewReader(conn)
	protocol, err := detectInboundProtocol(reader)
	if err != nil {
		log.Printf("[mixed] detect protocol from %s failed: %v", conn.RemoteAddr(), err)
		conn.Close()
		return
	}

	wrappedConn := &bufferedConn{
		Conn:   conn,
		reader: reader,
	}

	if protocol == inboundProtocolSOCKS5 {
		s.socks5Server.handleConnection(wrappedConn)
		return
	}

	if err := httpListener.Dispatch(wrappedConn); err != nil {
		log.Printf("[mixed] hand off HTTP connection failed: %v", err)
		conn.Close()
	}
}

// detectInboundProtocol 通过首字节快速区分 SOCKS5 握手和 HTTP 请求。
func detectInboundProtocol(reader *bufio.Reader) (inboundProtocol, error) {
	firstByte, err := reader.Peek(1)
	if err != nil {
		return "", err
	}
	if firstByte[0] == 0x05 {
		return inboundProtocolSOCKS5, nil
	}
	return inboundProtocolHTTP, nil
}

// bufferedConn 让上层仍能读到 Peek 过的首包，避免协议探测吞掉字节。
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

// connChanListener 把外部接收的连接转交给 net/http.Server 复用。
type connChanListener struct {
	addr      net.Addr
	conns     chan net.Conn
	closeOnce sync.Once
	done      chan struct{}
}

func newConnChanListener(addr net.Addr) *connChanListener {
	return &connChanListener{
		addr:  addr,
		conns: make(chan net.Conn, 64),
		done:  make(chan struct{}),
	}
}

func (l *connChanListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case conn := <-l.conns:
		if conn == nil {
			return nil, net.ErrClosed
		}
		return conn, nil
	}
}

func (l *connChanListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.done)
	})
	return nil
}

func (l *connChanListener) Addr() net.Addr {
	return l.addr
}

func (l *connChanListener) Dispatch(conn net.Conn) error {
	select {
	case <-l.done:
		return net.ErrClosed
	case l.conns <- conn:
		return nil
	}
}
