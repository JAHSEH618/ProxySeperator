package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
)

type SOCKS5Server struct {
	address  string
	dialer   Dialer
	logger   *logging.Logger
	listener net.Listener
}

func NewSOCKS5Server(address string, dialer Dialer, logger *logging.Logger) *SOCKS5Server {
	return &SOCKS5Server{address: address, dialer: dialer, logger: logger}
}

func (s *SOCKS5Server) Start(context.Context) error {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return api.WrapError(api.ErrCodeProxyListenFailed, "SOCKS5 代理监听失败", err)
	}
	s.listener = listener
	s.logger.Info("proxy.socks5", "SOCKS5 代理已启动", map[string]any{"address": s.address})
	go s.acceptLoop()
	return nil
}

func (s *SOCKS5Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.address
}

func (s *SOCKS5Server) Stop() error {
	if s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

func (s *SOCKS5Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *SOCKS5Server) handleConn(client net.Conn) {
	defer client.Close()

	header := make([]byte, 2)
	if _, err := io.ReadFull(client, header); err != nil {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	_, _ = client.Write([]byte{0x05, 0x00})

	req := make([]byte, 4)
	if _, err := io.ReadFull(client, req); err != nil {
		return
	}
	if req[1] != 0x01 {
		_, _ = client.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	target, err := readTargetAddress(client, req[3])
	if err != nil {
		_, _ = client.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	destConn, _, err := s.dialer.DialTarget(context.Background(), "tcp", target)
	if err != nil {
		_, _ = client.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer destConn.Close()

	_, _ = client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	go copyAndClose(destConn, client)
	copyAndClose(client, destConn)
}

func readTargetAddress(r io.Reader, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		buf := make([]byte, 6)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		host := net.IP(buf[:4]).String()
		port := binary.BigEndian.Uint16(buf[4:])
		return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
	case 0x03:
		lb := []byte{0}
		if _, err := io.ReadFull(r, lb); err != nil {
			return "", err
		}
		buf := make([]byte, int(lb[0])+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		host := string(buf[:len(buf)-2])
		port := binary.BigEndian.Uint16(buf[len(buf)-2:])
		return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
	case 0x04:
		buf := make([]byte, 18)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		host := net.IP(buf[:16]).String()
		port := binary.BigEndian.Uint16(buf[16:])
		return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
	default:
		return "", api.NewError(api.ErrCodeInvalidConfig, "不支持的 SOCKS5 地址类型")
	}
}
