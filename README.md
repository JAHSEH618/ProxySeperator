<div align="center">

# ProxySeparator

**Smart dual-proxy traffic splitter for developers**

Route company traffic through your corporate proxy and everything else through your personal proxy — automatically.

[English](./README.md) | [中文](./README_zh.md)

</div>

---

## Why ProxySeparator?

If you work behind a corporate VPN/proxy but also run a personal proxy (Clash, V2Ray, etc.), you know the pain: you can't use both at the same time. ProxySeparator sits between your apps and both upstream proxies, routing traffic to the right one based on rules you define.

```
┌─────────────┐     ┌──────────────────┐     ┌───────────────────┐
│  Your Apps   │────▶│  ProxySeparator  │────▶│  Company Proxy    │  ← .company.com, 10.0.0.0/8
│  (Browser,   │     │                  │     └───────────────────┘
│   Terminal,  │     │  HTTP  :17900    │
│   IDE...)    │     │  SOCKS5:17901    │     ┌───────────────────┐
└─────────────┘     │  DNS   :18553    │────▶│  Personal Proxy   │  ← everything else
                    └──────────────────┘     └───────────────────┘
```

## Features

- **Rule-based routing** — Domain suffix, exact match, keyword, and IP CIDR rules
- **Dual proxy mode** — System proxy (auto-configures OS settings) or TUN mode (captures all traffic)
- **Protocol auto-detection** — Automatically detects HTTP/SOCKS5 upstream protocol
- **Real-time dashboard** — Live traffic stats, upstream health checks, and connection monitoring
- **Crash recovery** — Recovery journal ensures your network settings are restored after unexpected exits
- **Pre-flight checks** — Validates configuration and upstream reachability before starting
- **Route tester** — Test which upstream a domain/IP will be routed to before going live
- **Cross-platform** — macOS and Windows support

## Architecture

Five-layer backend with clear separation of concerns:

```
┌─────────────────────────────────────────────────┐
│  Frontend  (React 19 + TypeScript + Vite)       │  UI & user interaction
├─────────────────────────────────────────────────┤
│  App Layer        (internal/app/)               │  Wails bindings & coordination
├─────────────────────────────────────────────────┤
│  API Types        (internal/api/)               │  Shared data contract
├─────────────────────────────────────────────────┤
│  Runtime Layer    (internal/runtime/)            │  Proxy lifecycle & state machine
├─────────────────────────────────────────────────┤
│  Platform Layer   (internal/platform/)           │  OS-specific operations
├─────────────────────────────────────────────────┤
│  Domain Packages  (rules, proxy, dns, config)   │  Core business logic
└─────────────────────────────────────────────────┘
```

## Getting Started

### Prerequisites

- **Go** 1.25+
- **Node.js** (for frontend build)
- **Wails v3** CLI (for development)

### Install & Run (Development)

```bash
# Clone the repository
git clone https://github.com/friedhelmliu/ProxySeperator.git
cd ProxySeperator

# Install frontend dependencies
cd frontend && npm install && cd ..

# Start development environment (frontend watch + backend)
./start.sh

# Stop
./stop.sh

# Restart
./restart.sh
```

### Build from Source

```bash
# Build frontend
cd frontend && npm run build && cd ..

# Build backend
go build -o proxyseparator ./cmd/proxyseparator

# Run
./proxyseparator
```

## Automated GitHub Releases

This repository includes an automated release workflow in `.github/workflows/release.yml`. When you push a tag such as `v0.1.0`, GitHub Actions will build and publish release artifacts automatically.

- macOS: separate unsigned `.dmg` files for Intel (`macos-13`) and Apple Silicon (`macos-14`)
- Windows: unsigned NSIS installer `.exe` with `wintun.dll` bundled in
- GitHub Release: uploads all artifacts to the matching Release automatically

Typical flow:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Notes:

- The current workflow produces unsigned packages. Add macOS signing/notarization and Windows code signing secrets when you are ready for production distribution.
- `build/appicon.png` is still the default placeholder icon. Replace it before shipping publicly.

### Run Tests

```bash
# All tests
go test ./...

# Specific package
go test ./internal/rules/...
go test ./internal/runtime/...
```

## Configuration

Config is stored at `~/.config/ProxySeparator/config.json`. The app provides a GUI for all settings, but here's the structure for reference:

| Setting | Default | Description |
|---------|---------|-------------|
| Company upstream | `system-route` (direct) | Corporate proxy or direct routing via OS routes |
| Personal upstream | `127.0.0.1:7897` | Your personal proxy (Clash, V2Ray, etc.) |
| Mode | `system` | `system` (OS proxy settings) or `tun` (virtual interface) |
| Language | `zh-CN` | UI language |
| Theme | `system` | `system`, `light`, or `dark` |

## Rule Syntax

Rules determine which traffic goes to the company upstream. Everything else goes to the personal upstream.

### Shorthand (recommended)

```
.company.com          # Domain suffix — matches *.company.com
jira.mycompany.io     # Exact domain
corp                  # Keyword — matches any domain containing "corp"
10.0.0.0/8            # IP CIDR range
172.16.0.0/12         # Private network range
192.168.0.0/16        # Local network range
```

### Explicit format

```
domain-suffix,company.com
domain-exact,jira.mycompany.io
domain-keyword,corpnet
ip-cidr,10.0.0.0/8
```

### Rule types

| Type | Shorthand Example | Matches |
|------|-------------------|---------|
| `DOMAIN_SUFFIX` | `.company.com` | `*.company.com` and `company.com` |
| `DOMAIN_EXACT` | `jira.corp.io` | Exactly `jira.corp.io` |
| `DOMAIN_KEYWORD` | `corp` | Any domain containing `corp` |
| `IP_CIDR` | `10.0.0.0/8` | IPs in the `10.x.x.x` range |

## Tech Stack

| Component | Technology |
|-----------|------------|
| Backend | Go 1.25 |
| Frontend | React 19, TypeScript, Vite |
| Desktop Framework | [Wails v3](https://wails.io/) |
| DNS | [miekg/dns](https://github.com/miekg/dns) |
| TUN | [tun2socks](https://github.com/xjasonlyu/tun2socks) |
| Network Stack | [gVisor netstack](https://gvisor.dev/) |

## Project Structure

```
├── cmd/proxyseparator/     # Entry point (app + TUN helper)
├── internal/
│   ├── app/                # Wails bindings & backend API
│   ├── api/                # Shared types & error codes
│   ├── runtime/            # Proxy lifecycle, forwarding, stats
│   ├── platform/           # OS adapters (macOS, Windows)
│   ├── rules/              # Rule parser & matcher (trie + CIDR)
│   ├── proxy/              # HTTP & SOCKS5 inbound listeners
│   ├── dns/                # Local DNS server & cache
│   ├── config/             # JSON persistence & migration
│   ├── logging/            # Structured logger & ring buffer
│   └── tunhelper/          # TUN subprocess orchestration
├── frontend/
│   └── src/                # React app (TypeScript)
├── start.sh                # Dev: start frontend watch + backend
├── stop.sh                 # Dev: stop all processes
├── restart.sh              # Dev: restart
└── fix-network.sh          # Manual network recovery utility
```

## How It Works

1. **Start** — ProxySeparator runs pre-flight checks, starts HTTP (`:17900`) and SOCKS5 (`:17901`) proxy listeners, and a local DNS server (`:18553`)
2. **System proxy mode** — Configures your OS to route traffic through ProxySeparator's local proxies
3. **TUN mode** — Creates a virtual network interface to capture all traffic (requires admin privileges)
4. **Route** — For each connection, the rules engine checks the destination against your rules using a domain trie and CIDR matching
5. **Forward** — Company-matched traffic is sent to the company upstream; everything else goes to the personal upstream
6. **Monitor** — Real-time health checks, traffic stats, and session tracking are pushed to the frontend via events
7. **Stop** — Recovery journal ensures OS network settings are cleanly restored, even after crashes
