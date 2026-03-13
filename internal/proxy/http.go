package proxy

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
	"github.com/friedhelmliu/ProxySeperator/internal/logging"
)

type Dialer interface {
	DialTarget(ctx context.Context, network, addr string) (net.Conn, string, error)
}

type HTTPServer struct {
	address string
	dialer  Dialer
	logger  *logging.Logger

	listener net.Listener
	server   *http.Server
}

func NewHTTPServer(address string, dialer Dialer, logger *logging.Logger) *HTTPServer {
	return &HTTPServer{
		address: address,
		dialer:  dialer,
		logger:  logger,
	}
}

func (s *HTTPServer) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return api.WrapError(api.ErrCodeProxyListenFailed, "HTTP 代理监听失败", err)
	}
	s.listener = listener

	s.server = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		_ = s.server.Serve(listener)
	}()
	s.logger.Info("proxy.http", "HTTP 代理已启动", map[string]any{"address": s.address})
	return nil
}

func (s *HTTPServer) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.address
}

func (s *HTTPServer) Stop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *HTTPServer) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		s.handleConnect(rw, req)
		return
	}
	s.handleForward(rw, req)
}

func (s *HTTPServer) handleConnect(rw http.ResponseWriter, req *http.Request) {
	destConn, _, err := s.dialer.DialTarget(req.Context(), "tcp", req.Host)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}

	hijacker, ok := rw.(http.Hijacker)
	if !ok {
		http.Error(rw, "hijack not supported", http.StatusInternalServerError)
		_ = destConn.Close()
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		_ = destConn.Close()
		return
	}

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go copyAndClose(destConn, clientConn)
	go copyAndClose(clientConn, destConn)
}

func (s *HTTPServer) handleForward(rw http.ResponseWriter, req *http.Request) {
	outReq := req.Clone(req.Context())
	outReq.RequestURI = ""
	if outReq.URL.Scheme == "" {
		outReq.URL.Scheme = "http"
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, _, err := s.dialer.DialTarget(ctx, network, addr)
			return conn, err
		},
	}

	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			rw.Header().Add(key, value)
		}
	}
	rw.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(rw, resp.Body)
}

func copyAndClose(dst io.WriteCloser, src io.ReadCloser) {
	defer dst.Close()
	defer src.Close()
	_, _ = io.Copy(dst, bufio.NewReader(src))
}
