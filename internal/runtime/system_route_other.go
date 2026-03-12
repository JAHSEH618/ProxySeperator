//go:build !darwin

package runtime

import "context"

func defaultLookupSystemRouteAddrs(context.Context, string) ([]string, error) {
	return nil, nil
}
