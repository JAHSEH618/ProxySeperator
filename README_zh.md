<div align="center">

# ProxySeparator

**智能双代理流量分流器**

自动将公司流量路由到公司代理，其他流量路由到个人代理。

[English](./README.md) | [中文](./README_zh.md)

</div>

---

## 为什么需要 ProxySeparator？

如果你在公司使用 VPN 或公司代理，同时又有自己的代理工具（Clash、V2Ray 等），你一定遇到过这个问题：两个代理无法同时使用。ProxySeparator 在你的应用和两个上游代理之间充当智能中间层，根据你定义的规则自动分流。

```
┌────────────┐     ┌──────────────────┐     ┌────────────┐
│  你的应用  │────▶│  ProxySeparator  │────▶│  公司代理  │  ← .company.com, 10.0.0.0/8
│  (浏览器,  │     │                  │     └────────────┘
│   终端,    │     │  HTTP  :17900    │
│   IDE...)  │     │  SOCKS5:17901    │     ┌────────────┐
└────────────┘     │  DNS   :18553    │────▶│  个人代理  │  ← 其他所有流量
                   └──────────────────┘     └────────────┘
```

## 核心功能

- **规则分流** — 支持域名后缀、精确域名、关键字、IP CIDR 四种规则类型
- **双代理模式** — 系统代理模式（自动配置操作系统设置）或 TUN 模式（虚拟网卡捕获全部流量）
- **协议自动探测** — 自动识别上游是 HTTP 还是 SOCKS5 协议
- **实时监控面板** — 实时流量统计、上游健康检查、连接数监控
- **崩溃恢复** — 通过恢复日志确保异常退出后网络设置被正确还原
- **启动前检查** — 启动前验证配置和上游可达性
- **路由测试** — 测试某个域名/IP 会被路由到哪个上游
- **跨平台** — 支持 macOS 和 Windows

## 架构

后端采用五层分层架构：

```
┌─────────────────────────────────────────────────┐
│  前端          (React 19 + TypeScript + Vite)     │  界面与用户交互
├─────────────────────────────────────────────────┤
│  应用层      (internal/app/)                    │  Wails 绑定与协调
├─────────────────────────────────────────────────┤
│  API 类型层  (internal/api/)                    │  前后端共享数据契约
├─────────────────────────────────────────────────┤
│  运行时层    (internal/runtime/)                │  代理生命周期与状态机
├─────────────────────────────────────────────────┤
│  平台层      (internal/platform/)               │  操作系统适配
├─────────────────────────────────────────────────┤
│  领域包      (rules, proxy, dns, config)        │  核心业务逻辑
└─────────────────────────────────────────────────┘
```

## 快速开始

### 环境要求

- **Go** 1.25+
- **Node.js**（用于前端构建）
- **Wails v3** CLI（用于开发）

### 开发模式

```bash
# 克隆仓库
git clone https://github.com/friedhelmliu/ProxySeperator.git
cd ProxySeperator

# 安装前端依赖
cd frontend && npm install && cd ..

# 启动开发环境（前端热更新 + 后端）
./start.sh

# 停止
./stop.sh

# 重启
./restart.sh
```

### 从源码构建

```bash
# 构建前端
cd frontend && npm run build && cd ..

# 构建后端
go build -o proxyseparator ./cmd/proxyseparator

# 运行
./proxyseparator
```

### 运行测试

```bash
# 全部测试
go test ./...

# 单个包
go test ./internal/rules/...
go test ./internal/runtime/...
```

## 配置说明

配置文件位于 `~/.config/ProxySeparator/config.json`，应用提供图形界面管理所有设置。

| 设置 | 默认值 | 说明 |
|------|--------|------|
| 公司上游 | `system-route`（直连） | 通过操作系统路由表直接连接公司网络 |
| 个人上游 | `127.0.0.1:7897` | 你的个人代理地址（Clash、V2Ray 等） |
| 代理模式 | `system` | `system`（系统代理）或 `tun`（虚拟网卡） |
| 界面语言 | `zh-CN` | 界面语言 |
| 主题 | `system` | `system`、`light` 或 `dark` |

## 规则语法

规则决定哪些流量走公司上游，未匹配的流量走个人上游。

### 简写格式（推荐）

```
.company.com          # 域名后缀 — 匹配 *.company.com
jira.mycompany.io     # 精确域名
corp                  # 关键字 — 匹配包含 "corp" 的域名
10.0.0.0/8            # IP CIDR 范围
172.16.0.0/12         # 内网地址段
192.168.0.0/16        # 局域网地址段
```

### 显式格式

```
domain-suffix,company.com
domain-exact,jira.mycompany.io
domain-keyword,corpnet
ip-cidr,10.0.0.0/8
```

### 规则类型

| 类型 | 简写示例 | 匹配范围 |
|------|----------|----------|
| `DOMAIN_SUFFIX` | `.company.com` | `*.company.com` 及 `company.com` |
| `DOMAIN_EXACT` | `jira.corp.io` | 精确匹配 `jira.corp.io` |
| `DOMAIN_KEYWORD` | `corp` | 包含 `corp` 的所有域名 |
| `IP_CIDR` | `10.0.0.0/8` | `10.x.x.x` 范围内的 IP |

## 技术栈

| 组件 | 技术 |
|------|------|
| 后端 | Go 1.25 |
| 前端 | React 19、TypeScript、Vite |
| 桌面框架 | [Wails v3](https://wails.io/) |
| DNS | [miekg/dns](https://github.com/miekg/dns) |
| TUN | [tun2socks](https://github.com/xjasonlyu/tun2socks) |
| 网络栈 | [gVisor netstack](https://gvisor.dev/) |

## 项目结构

```
├── cmd/proxyseparator/     # 入口（应用 + TUN 辅助进程）
├── internal/
│   ├── app/                # Wails 绑定与后端 API
│   ├── api/                # 共享类型与错误码
│   ├── runtime/            # 代理生命周期、转发、统计
│   ├── platform/           # 操作系统适配器（macOS、Windows）
│   ├── rules/              # 规则解析与匹配（字典树 + CIDR）
│   ├── proxy/              # HTTP 与 SOCKS5 入站监听
│   ├── dns/                # 本地 DNS 服务器与缓存
│   ├── config/             # JSON 配置持久化与迁移
│   ├── logging/            # 结构化日志与环形缓冲区
│   └── tunhelper/          # TUN 子进程管理
├── frontend/
│   └── src/                # React 应用（TypeScript）
├── start.sh                # 开发：启动前端热更新 + 后端
├── stop.sh                 # 开发：停止所有进程
├── restart.sh              # 开发：重启
└── fix-network.sh          # 手动网络恢复工具
```

## 工作原理

1. **启动** — 运行启动前检查，启动 HTTP (`:17900`) 和 SOCKS5 (`:17901`) 代理监听，以及本地 DNS 服务器 (`:18553`)
2. **系统代理模式** — 自动配置操作系统代理设置，将流量导向 ProxySeparator 的本地代理
3. **TUN 模式** — 创建虚拟网卡捕获所有流量（需要管理员权限）
4. **路由** — 对每个连接，规则引擎使用字典树和 CIDR 匹配检查目标地址
5. **转发** — 匹配公司规则的流量发往公司上游，其余发往个人上游
6. **监控** — 实时健康检查、流量统计、会话追踪通过事件推送到前端
7. **停止** — 恢复日志确保操作系统网络设置被正确还原，即使发生崩溃也能恢复
