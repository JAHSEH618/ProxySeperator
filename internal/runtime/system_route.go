package runtime

import (
	"context"
	"net"
	"time"
)

var lookupSystemRouteAddrs = defaultLookupSystemRouteAddrs
var systemRouteDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
}

func dialSystemRoute(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := systemRouteDialContext(ctx, network, addr)
	if err == nil {
		return conn, nil
	}

	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil || net.ParseIP(host) != nil {
		return nil, err
	}

	addrs, lookupErr := lookupSystemRouteAddrs(ctx, host)
	if lookupErr != nil || len(addrs) == 0 {
		return nil, err
	}

	lastErr := err
	for _, ip := range addrs {
		candidate := net.JoinHostPort(ip, port)
		conn, dialErr := systemRouteDialContext(ctx, network, candidate)
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}

	return nil, lastErr
}
