package platform

import (
	"fmt"
	"strings"
)

func splitHostPort(value string) (string, string, error) {
	parts := strings.Split(value, ":")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid address %q", value)
	}
	host := strings.Join(parts[:len(parts)-1], ":")
	port := parts[len(parts)-1]
	return host, port, nil
}

func maxInt(values ...int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}
