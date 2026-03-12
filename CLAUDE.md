# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

ProxySeparator is a macOS/Windows desktop app that splits network traffic between two upstream proxies: company traffic goes to a company proxy, everything else goes to a personal proxy. Built with Go backend + Wails v3 + React frontend.

The binary doubles as a TUN helper process when invoked with `tun-helper` as the first argument (`cmd/proxyseparator/main.go`).

## Build & Run

**Prerequisites:** Go 1.25+, Node.js (for frontend)

```bash
# Install frontend dependencies (first time)
cd frontend && npm install && cd ..

# Development: builds frontend + backend, runs with hot-reload watch
./start.sh        # starts frontend watch + backend
./stop.sh         # stops both processes
./restart.sh      # stop + start

# Manual build
cd frontend && npm run build && cd ..
go build -o proxyseparator ./cmd/proxyseparator

# Run tests
go test ./...

# Run a single package's tests
go test ./internal/rules/...
go test ./internal/runtime/...
```

Dev runtime state (PIDs, logs, binaries) lives in `.run/` which is gitignored.

## Architecture

Five-layer backend:

1. **App layer** (`internal/app/`) — `BackendAPI` struct exposes all methods to the Wails frontend via bindings. Coordinates config, runtime, and logging. The `BackendAPIInterface` in `bindings.go` defines the contract.

2. **API types** (`internal/api/`) — Shared types (`Config`, `RuntimeStatus`, `HealthStatus`, `TrafficStats`, `LogEntry`, etc.) and error codes. This is the data contract between frontend and backend. Constants for modes (`system`/`tun`), states (`idle`/`starting`/`running`/`stopping`/`error`), protocols, and event names live here.

3. **Runtime layer** (`internal/runtime/`) — `Manager` orchestrates the full lifecycle: start/stop state machine, HTTP+SOCKS5 proxy listeners (`:17900`/`:17901`), DNS server (`:18553`), forwarding decisions, upstream health checks, traffic stats, system route management, and recovery journal for crash recovery.

4. **Platform layer** (`internal/platform/`) — `Controller` interface abstracts OS-specific operations (system proxy, TUN, auto-start, network recovery). Implementations: `adapter_darwin.go` (macOS via `networksetup`), `adapter_windows.go` (Windows registry). `tun_helper_process.go` runs the TUN stack via `tun2socks` in a privileged subprocess.

5. **Domain packages:**
   - `internal/rules/` — Rule parsing (`parser.go`), matching engine (`matcher.go`) using domain trie + CIDR. Supports DOMAIN_SUFFIX, DOMAIN_EXACT, DOMAIN_KEYWORD, IP_CIDR rule types.
   - `internal/proxy/` — HTTP proxy (`http.go`) and SOCKS5 proxy (`socks5.go`) inbound listeners.
   - `internal/dns/` — Local DNS server + cache for TUN mode domain-to-IP mapping.
   - `internal/config/` — JSON config persistence via `Store`, migration logic.
   - `internal/logging/` — Structured logger with ring buffer for in-app log viewer.

**Frontend** (`frontend/`): React 19 + TypeScript + Vite. Communicates with Go via Wails bindings and events (`runtime:status`, `runtime:health`, `runtime:traffic`, `runtime:log`). Built to `frontend/dist/`, which the Go binary serves as embedded assets.

## Key Patterns

- The runtime `Manager` is the single point of control for mode switching — all state transitions are serialized through its mutex.
- Platform operations that modify system state (proxy settings, TUN) write a recovery journal (`recovery-journal.json`) so the app can clean up on crash/restart.
- Company upstream defaults to `system-route` (direct via OS routing table) rather than a proxy port. Personal upstream defaults to `127.0.0.1:7897`.
- Frontend events are pushed via `dynamicEmitter` — the emitter function is set after Wails app initialization since it depends on `app.EmitEvent`.
- After privileged operations (Start/Stop/RecoverNetwork), `restoreWindow()` is called to bring the window back to foreground (macOS auth dialogs steal focus).
- Log messages are in Chinese (zh-CN is the default locale).
