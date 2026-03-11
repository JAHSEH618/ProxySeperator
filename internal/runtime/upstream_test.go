package runtime

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/friedhelmliu/ProxySeperator/internal/api"
)

func TestProbeUpstreamDetectsSOCKS5(t *testing.T) {
	address := startSOCKS5Stub(t)
	health := ProbeUpstream(context.Background(), api.UpstreamConfig{
		Host:     "127.0.0.1",
		Port:     mustPort(t, address),
		Protocol: api.ProtocolAuto,
	})
	if !health.Reachable || health.Protocol != api.ProtocolSOCKS5 {
		t.Fatalf("unexpected health: %+v", health)
	}
}

func TestProbeUpstreamDetectsHTTPProxy(t *testing.T) {
	address := startHTTPProxyStub(t)
	health := ProbeUpstream(context.Background(), api.UpstreamConfig{
		Host:     "127.0.0.1",
		Port:     mustPort(t, address),
		Protocol: api.ProtocolAuto,
	})
	if !health.Reachable || health.Protocol != api.ProtocolHTTP {
		t.Fatalf("unexpected health: %+v", health)
	}
}

func TestProbeUpstreamTreatsDirectTransportAsReachable(t *testing.T) {
	health := ProbeUpstream(context.Background(), api.UpstreamConfig{
		Protocol: api.ProtocolDirect,
	})
	if !health.Reachable || health.Protocol != api.ProtocolDirect {
		t.Fatalf("unexpected health: %+v", health)
	}
}

func startSOCKS5Stub(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 3)
				_, _ = c.Read(buf)
				_, _ = c.Write([]byte{0x05, 0x00})
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func startHTTPProxyStub(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				line, _ := bufio.NewReader(c).ReadString('\n')
				if strings.HasPrefix(line, "CONNECT ") {
					_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func mustPort(t *testing.T, address string) int {
	t.Helper()
	_, portString, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	_, err = fmt.Sscanf(portString, "%d", &port)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
