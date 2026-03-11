//go:build darwin

package runtime

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

func defaultLookupSystemRouteAddrs(ctx context.Context, host string) ([]string, error) {
	output, err := exec.CommandContext(ctx, "dscacheutil", "-q", "host", "-a", "name", host).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("lookup host with dscacheutil: %w", err)
	}

	seen := map[string]struct{}{}
	addrs := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "ip_address:"):
			ip := strings.TrimSpace(strings.TrimPrefix(line, "ip_address:"))
			if parsed := net.ParseIP(ip); parsed != nil {
				if _, ok := seen[ip]; !ok {
					seen[ip] = struct{}{}
					addrs = append(addrs, ip)
				}
			}
		case strings.HasPrefix(line, "ipv6_address:"):
			ip := strings.TrimSpace(strings.TrimPrefix(line, "ipv6_address:"))
			if parsed := net.ParseIP(ip); parsed != nil {
				if _, ok := seen[ip]; !ok {
					seen[ip] = struct{}{}
					addrs = append(addrs, ip)
				}
			}
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no system-route address found for %q", host)
	}
	return addrs, nil
}
