//go:build darwin

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
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
	// Before each iteration we check the FIFO still exists to avoid spinning if deleted.
	b.WriteString("(\n")
	b.WriteString("  trap 'rm -f \"$FIFO\"; exit 0' TERM INT HUP EXIT\n")
	b.WriteString("  while true; do\n")
	b.WriteString("    if [ ! -p \"$FIFO\" ]; then\n")
	b.WriteString("      exit 0\n")
	b.WriteString("    fi\n")
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
	b.WriteString("        PING)\n")
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
// Returns an error if the expected fields are missing or malformed.
func parseRouteHelperOutput(output string) (fifoPath string, pid int, err error) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "FIFO:") {
			fifoPath = strings.TrimSpace(strings.TrimPrefix(line, "FIFO:"))
		}
		if strings.HasPrefix(line, "PID:") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "PID:"))
			if v, parseErr := strconv.Atoi(raw); parseErr == nil && v > 0 {
				pid = v
			}
		}
	}
	if fifoPath == "" && pid == 0 {
		err = fmt.Errorf("路由助手输出中未找到 FIFO 和 PID 字段，原始输出: %q", output)
	} else if fifoPath == "" {
		err = fmt.Errorf("路由助手输出中未找到 FIFO 字段，原始输出: %q", output)
	} else if pid <= 0 {
		err = fmt.Errorf("路由助手输出中未找到有效 PID，原始输出: %q", output)
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
	if err != nil {
		// FIFO broken — daemon likely dead. Clean up.
		_ = h.fifoFD.Close()
		h.fifoFD = nil
		if h.fifoPath != "" {
			_ = os.Remove(h.fifoPath)
			h.fifoPath = ""
		}
		h.pid = 0
		return fmt.Errorf("route helper FIFO write failed (daemon may have exited): %w", err)
	}
	return nil
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

// stop sends EXIT and cleans up resources. If the FIFO is broken,
// falls back to SIGTERM/SIGKILL to ensure the helper process is terminated.
// Also cleans up orphaned FIFO and script files from crashed sessions.
func (h *routeHelper) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 1. Try graceful EXIT via FIFO.
	if h.fifoFD != nil {
		_, _ = fmt.Fprintln(h.fifoFD, "EXIT")
		_ = h.fifoFD.Close()
		h.fifoFD = nil
	}

	// 2. If we have a PID, ensure the process is dead.
	//    The FIFO may already be broken, so we cannot rely on EXIT alone.
	if h.pid > 0 {
		alive, _ := processRunning(h.pid)
		if alive {
			_ = syscall.Kill(h.pid, syscall.SIGTERM)
			// Wait up to 2 seconds for graceful exit.
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
				if still, _ := processRunning(h.pid); !still {
					break
				}
			}
			// Force kill if still alive.
			if still, _ := processRunning(h.pid); still {
				_ = syscall.Kill(h.pid, syscall.SIGKILL)
			}
		}
		h.pid = 0
	}

	// 3. Remove the FIFO file.
	if h.fifoPath != "" {
		_ = os.Remove(h.fifoPath)
		h.fifoPath = ""
	}

	// 4. Clean up orphaned FIFOs and privileged scripts from crashed sessions.
	cleanupOrphanedFiles()
}

func (h *routeHelper) running() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fifoFD == nil {
		return false
	}
	// Check if daemon process is still alive.
	if h.pid > 0 {
		alive, _ := processRunning(h.pid)
		if !alive {
			// Daemon died — clean up state.
			_ = h.fifoFD.Close()
			h.fifoFD = nil
			if h.fifoPath != "" {
				_ = os.Remove(h.fifoPath)
				h.fifoPath = ""
			}
			h.pid = 0
			return false
		}
	}
	// Verify FIFO still exists on disk.
	if h.fifoPath != "" {
		if info, err := os.Stat(h.fifoPath); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			// FIFO file gone or replaced — clean up.
			_ = h.fifoFD.Close()
			h.fifoFD = nil
			h.fifoPath = ""
			if h.pid > 0 {
				_ = syscall.Kill(h.pid, syscall.SIGTERM)
				h.pid = 0
			}
			return false
		}
	}
	return true
}

// cleanupOrphanedFiles scans /tmp for leftover route helper FIFOs and
// privileged scripts from previous crashed sessions.
func cleanupOrphanedFiles() {
	// Clean up orphaned FIFO files.
	fifoMatches, _ := filepath.Glob("/tmp/proxyseparator-route-helper-*")
	for _, path := range fifoMatches {
		info, err := os.Stat(path)
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		if info.Mode()&os.ModeNamedPipe == 0 {
			// Not a FIFO — leftover temp file, remove.
			_ = os.Remove(path)
			continue
		}
		// Try to open FIFO non-blocking to check if there's a reader.
		// ENXIO means no reader — orphaned FIFO, safe to remove.
		fd, openErr := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if openErr != nil {
			// No reader — orphaned FIFO.
			_ = os.Remove(path)
			continue
		}
		// A reader is active. Leave it alone.
		_ = syscall.Close(fd)
	}

	// Clean up orphaned privileged script files.
	scriptMatches, _ := filepath.Glob("/tmp/proxyseparator-priv-*.sh")
	for _, path := range scriptMatches {
		_ = os.Remove(path)
	}
}

// killHelperByPID attempts to kill a helper process by PID (best-effort).
func killHelperByPID(pid int) {
	if pid <= 0 {
		return
	}
	alive, _ := processRunning(pid)
	if !alive {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
}
