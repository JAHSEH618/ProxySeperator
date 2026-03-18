//go:build darwin

package platform

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// routeHelper is a long-lived privileged process that accepts route
// commands via a FIFO, eliminating repeated admin password prompts.
// It is started alongside the initial static route application (one prompt)
// and stays alive for the session.
type routeHelper struct {
	mu       sync.Mutex
	fifoPath string
	pid      int
	fifoFD   *os.File
}

// buildRouteHelperScript returns a shell script that:
// 1. Applies the given initial routes
// 2. Starts a background daemon reading route commands from a FIFO
// 3. Prints the FIFO path and daemon PID, then exits
func buildRouteHelperScript(iface string, initialRoutes []string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n\n")

	// Apply initial static routes.
	sorted := append([]string(nil), initialRoutes...)
	sort.Strings(sorted)
	for _, prefix := range sorted {
		fmt.Fprintf(&b, "/sbin/route -n delete -net %s -interface %s 2>/dev/null || true\n", shellQuote(prefix), shellQuote(iface))
		fmt.Fprintf(&b, "/sbin/route -n add -net %s -interface %s >/dev/null || true\n", shellQuote(prefix), shellQuote(iface))
	}

	// Create FIFO for dynamic route commands.
	b.WriteString("\n")
	b.WriteString("FIFO=$(mktemp -u /tmp/proxyseparator-route-helper-XXXXXX)\n")
	b.WriteString("mkfifo \"$FIFO\" || { echo 'ERROR:mkfifo'; exit 1; }\n")
	b.WriteString("chmod 622 \"$FIFO\"\n") // owner rw, group/other write-only
	b.WriteString("\n")

	// Background daemon: read commands from FIFO.
	// The outer while-true loop handles writer disconnections (EOF restarts the inner read).
	b.WriteString("(\n")
	b.WriteString("  trap 'rm -f \"$FIFO\"; exit 0' TERM INT\n")
	b.WriteString("  while true; do\n")
	b.WriteString("    while IFS= read -r line; do\n")
	b.WriteString("      cmd=\"${line%%:*}\"\n")
	b.WriteString("      rest=\"${line#*:}\"\n")
	b.WriteString("      case \"$cmd\" in\n")
	b.WriteString("        ROUTE_ADD)\n")
	b.WriteString("          prefix=\"${rest%%:*}\"\n")
	b.WriteString("          iface=\"${rest#*:}\"\n")
	// Validate: prefix must look like CIDR, iface must be alphanumeric.
	b.WriteString("          if echo \"$prefix\" | /usr/bin/grep -qE '^[0-9a-fA-F.:]+/[0-9]+$' && ")
	b.WriteString("echo \"$iface\" | /usr/bin/grep -qE '^[a-zA-Z0-9]+$'; then\n")
	b.WriteString("            /sbin/route -n delete -net \"$prefix\" -interface \"$iface\" 2>/dev/null || true\n")
	b.WriteString("            /sbin/route -n add -net \"$prefix\" -interface \"$iface\" >/dev/null 2>&1 || true\n")
	b.WriteString("          fi\n")
	b.WriteString("          ;;\n")
	b.WriteString("        ROUTE_DEL)\n")
	b.WriteString("          prefix=\"${rest%%:*}\"\n")
	b.WriteString("          iface=\"${rest#*:}\"\n")
	b.WriteString("          if echo \"$prefix\" | /usr/bin/grep -qE '^[0-9a-fA-F.:]+/[0-9]+$' && ")
	b.WriteString("echo \"$iface\" | /usr/bin/grep -qE '^[a-zA-Z0-9]+$'; then\n")
	b.WriteString("            /sbin/route -n delete -net \"$prefix\" -interface \"$iface\" 2>/dev/null || true\n")
	b.WriteString("          fi\n")
	b.WriteString("          ;;\n")
	b.WriteString("        EXIT)\n")
	b.WriteString("          rm -f \"$FIFO\"\n")
	b.WriteString("          exit 0\n")
	b.WriteString("          ;;\n")
	b.WriteString("      esac\n")
	b.WriteString("    done < \"$FIFO\"\n")
	b.WriteString("  done\n")
	b.WriteString(") >/dev/null 2>&1 &\n")
	b.WriteString("HELPER_PID=$!\n")
	b.WriteString("\n")

	// Output metadata for Go to parse.
	b.WriteString("echo \"FIFO:$FIFO\"\n")
	b.WriteString("echo \"PID:$HELPER_PID\"\n")
	b.WriteString("echo 'OK'\n")
	return b.String()
}

// parseRouteHelperOutput parses the script output to extract FIFO path and PID.
func parseRouteHelperOutput(output string) (fifoPath string, pid int) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "FIFO:") {
			fifoPath = strings.TrimPrefix(line, "FIFO:")
		}
		if strings.HasPrefix(line, "PID:") {
			if v, err := strconv.Atoi(strings.TrimPrefix(line, "PID:")); err == nil {
				pid = v
			}
		}
	}
	return
}

// sendCommand writes a command to the route helper FIFO.
func (h *routeHelper) sendCommand(cmd string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fifoFD == nil {
		return fmt.Errorf("route helper not running")
	}
	_, err := fmt.Fprintln(h.fifoFD, cmd)
	return err
}

// addRoutes sends ROUTE_ADD commands for each route.
func (h *routeHelper) addRoutes(iface string, routes []string) error {
	for _, route := range routes {
		if err := h.sendCommand(fmt.Sprintf("ROUTE_ADD:%s:%s", route, iface)); err != nil {
			return err
		}
	}
	return nil
}

// removeRoutes sends ROUTE_DEL commands for each route.
func (h *routeHelper) removeRoutes(iface string, routes []string) error {
	for _, route := range routes {
		if err := h.sendCommand(fmt.Sprintf("ROUTE_DEL:%s:%s", route, iface)); err != nil {
			return err
		}
	}
	return nil
}

// stop sends EXIT and cleans up resources.
func (h *routeHelper) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fifoFD != nil {
		_, _ = fmt.Fprintln(h.fifoFD, "EXIT")
		_ = h.fifoFD.Close()
		h.fifoFD = nil
	}
	if h.fifoPath != "" {
		_ = os.Remove(h.fifoPath)
		h.fifoPath = ""
	}
	h.pid = 0
}

func (h *routeHelper) running() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fifoFD != nil
}
