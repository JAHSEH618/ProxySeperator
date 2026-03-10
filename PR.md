**产品设计文档：代理隔离器（ProxySeparator） v1.2（Go版）**

**版本**：V1.2（Go + Wails 锁定版）  
**设计日期**：2026年3月  
**目标平台**：macOS 12+ / Windows 10/11（同一套代码，一键打包 .dmg + .exe）  
**核心技术栈**：**纯 Go 后端 + Wails v3 前端**（性能碾压 Electron 10倍以上）  
**一句话目标**：让用户输入 3 个信息（公司端口 7890、个人端口 7897、公司域名规则），一键实现“公司流量走 7890、其他全部走 7897”，彻底解决两个 VPN 冲突。

### 1. 产品定位 & 价值主张
- **定位**：极简、本地、零配置的代理流量隔离神器，专为“公司全局VPN + 个人翻墙”冲突场景设计。
- **价值主张**：
  - 3 分钟配置完成，无需懂 YAML、Clash、Proxifier
  - Go 原生性能：内存 < 45MB、启动 < 0.5s、二进制 < 15MB（Electron 版 10 倍差距）
  - 支持 TUN 模式（连游戏、Docker、Electron App 都完美分流）
  - 完全离线、零数据上传、隐私安全
- **竞品差异**：
  - Proxifier：付费 + 规则复杂
  - Clash Verge：需手动写配置
  - 本产品：输入框傻瓜式 + Go 高性能引擎

### 2. 目标用户 & 使用场景
- 同时开公司 VPN（7890 全局）+ 个人 VPN（7897 规则）的程序员/企业员工
- 公司强制全局但想翻墙看 GitHub/YouTube
- 远程办公、出海团队

### 3. 核心功能（MVP 已完整定义，可直接开发）

#### 3.1 配置界面（唯一主窗口）
- **输入字段**（带默认值 + 自动检测）：
  - 公司代理端口：`7890`（自动 Ping 检测 HTTP/SOCKS5 是否存活）
  - 个人 VPN 端口：`7897`（同上）
  - 公司规则（多行 TextArea，支持粘贴）：
    ```
    .company.com
    .internal
    xxx.com.cn
    10.0.0.0/8
    172.16.0.0/12
    192.168.0.0/16
    ```
    支持自动解析格式：DOMAIN-SUFFIX、DOMAIN-KEYWORD、IP-CIDR、完整域名

- **高级开关**（默认关闭，一键开启）：
  - TUN 模式（Go wintun/utun 虚拟网卡）
  - UDP 转发（游戏/视频必开）
  - 绕过大陆 IP（内置最新 GeoIP.dat）
  - 开机自启（macOS LaunchAgent + Windows 注册表）

- **一键大按钮**：启动隔离（绿色）→ 停止（红色）
- **实时状态栏**：
  - 公司流量 / 个人流量（MB/s，Go 实时统计）
  - 两个上游端口存活状态（绿/红）
  - 当前模式（系统代理 / TUN）

#### 3.2 规则测试器（独立弹窗）
- 输入任意域名/IP → 立即显示“将走公司端口 7890”或“走个人端口 7897”

#### 3.3 系统托盘 / 菜单栏
- macOS：顶部菜单栏图标（毛玻璃）
- Windows：系统托盘
- 功能：一键启动/停止、打开设置、测试域名、退出

#### 3.4 其他特性
- 配置自动保存（JSON 文件）
- 错误提示（“公司端口未开启，请先启动公司VPN”）
- 日志查看（调试模式）

### 4. 技术架构（Go 版完整设计）

**架构图（文字版）**：
```
用户流量
   ↓
[Wails 前端 (React)] ←→ [Go 后端 (Wails Bind)]
                  │
            [规则引擎 (Go 纯实现)]
                  │
   ├─ 匹配公司规则 → 转发到 127.0.0.1:7890（公司上游）
   └─ 其他流量     → 转发到 127.0.0.1:7897（个人上游）
                  │
            [两种模式切换]
            ├── 系统代理模式（Go 设置 macOS networksetup / Windows WinINet）
            └── TUN 模式（github.com/sagernet/wintun + utun，原生高性能）
```

**详细技术选型**（全部 Go 原生，性能最优）：
- **前端**：Wails v3 + React 19 + TypeScript + TailwindCSS + shadcn/ui（Web 技术，但打包成原生）
- **后端核心**（Go）：
  - 规则引擎：纯 Go 实现（domain trie + ip cidr 匹配，<1μs/请求）
  - 代理转发：github.com/miekg/dns + net.Dial + goroutine 并发（支持 HTTP/SOCKS5/UDP）
  - TUN 模式：github.com/sagernet/wintun（Windows）+ golang.org/x/net/ipv4（macOS utun）
  - 系统代理设置：macOS `os/exec` 调用 networksetup；Windows `golang.org/x/sys/windows/registry`
  - GeoIP：内置 maxmind geoip2（离线）
  - 配置持久化：encoding/json + os.UserConfigDir
- **依赖库**（go.mod 已规划）：
  ```go
  github.com/wailsapp/wails/v3
  github.com/sagernet/wintun
  github.com/oschwald/geoip2-golang
  golang.org/x/sys
  github.com/miekg/dns
  ```
- **打包**：
  - `wails build` → macOS .dmg（带签名模板）
  - `wails build -platform windows/amd64` → 单文件 .exe（<15MB）
- **性能指标**（Go 保证）：
  - 内存：空闲 20-40MB，峰值 <80MB
  - 延迟：额外 <2ms（远低于 Electron）
  - CPU：高并发下 <5%

### 5. 用户使用流程（双平台一致）
1. 下载 .dmg / .exe
2. 双击打开（自动检测端口）
3. 输入规则 → 点“启动隔离”
4. 完成（托盘常驻）

### 6. 非功能需求
- **体积**：<15MB 单文件
- **启动时间**：<0.5秒
- **兼容性**：支持 SOCKS5 / HTTP 自动探测
- **安全性**：零联网、沙盒运行
- **国际化**：简中（默认）+ English
- **更新**：内置静默检查（可选）

### 7. 开发计划（一人开发时间表）
- Day 1-2：Wails 项目初始化 + UI 配置页面
- Day 3-5：Go 规则引擎 + 转发核心
- Day 6-7：TUN 模式 + 系统代理切换
- Day 8-9：托盘、实时流量统计、测试器
- Day 10：双平台打包 + 测试（macOS/Windows）
- 总计：**10 天** 可出 MVP 可发布版本

### 8. 后续迭代（V2.0）
- 进程分流（拖拽 .exe）
- 公司域名模板库（一键导入）
- 规则云同步（IT 部门下发）
- 暗黑模式 + 更多皮肤

---
