#!/bin/bash
# ProxySeparator 网络恢复脚本
# 用法: bash fix-network.sh
# 功能: 清除系统代理、还原 DNS、删除 TUN 残留路由

set -e

echo "=== ProxySeparator 网络恢复 ==="

# 获取所有网络服务（跳过标题行和被禁用的服务）
services=()
while IFS= read -r line; do
  line="$(echo "$line" | xargs)"
  [[ -z "$line" ]] && continue
  [[ "$line" == An\ asterisk* ]] && continue
  [[ "$line" == \** ]] && continue
  services+=("$line")
done < <(networksetup -listallnetworkservices 2>/dev/null)

if [ ${#services[@]} -eq 0 ]; then
  echo "未检测到网络服务，退出"
  exit 1
fi

echo "检测到 ${#services[@]} 个网络服务: ${services[*]}"

# 1. 关闭系统代理
echo ""
echo "[1/3] 关闭系统代理..."
for svc in "${services[@]}"; do
  networksetup -setwebproxystate "$svc" off 2>/dev/null || true
  networksetup -setsecurewebproxystate "$svc" off 2>/dev/null || true
  networksetup -setsocksfirewallproxystate "$svc" off 2>/dev/null || true
  echo "  ✓ $svc"
done

# 2. 还原 DNS 为 DHCP 默认
echo ""
echo "[2/3] 还原 DNS 为 DHCP 默认..."
for svc in "${services[@]}"; do
  networksetup -setdnsservers "$svc" Empty 2>/dev/null || true
  echo "  ✓ $svc"
done

# 3. 清除 TUN 残留（路由 + 接口）
echo ""
echo "[3/3] 清除 TUN 残留..."
cleaned=0
for iface in $(ifconfig -l 2>/dev/null); do
  case "$iface" in
    utun*)
      routes=$(netstat -rnf inet 2>/dev/null | grep -w "$iface" | awk '{print $1}' || true)
      if [ -n "$routes" ]; then
        echo "$routes" | while read -r rt; do
          sudo route -n delete "$rt" -interface "$iface" 2>/dev/null || true
        done
        sudo ifconfig "$iface" down 2>/dev/null || true
        echo "  ✓ 清理 $iface 路由并关闭接口"
        cleaned=1
      fi
      ;;
  esac
done
if [ "$cleaned" -eq 0 ]; then
  echo "  无 TUN 残留"
fi

# 4. 刷新 DNS 缓存
echo ""
echo "刷新 DNS 缓存..."
sudo dscacheutil -flushcache 2>/dev/null || true
sudo killall -HUP mDNSResponder 2>/dev/null || true

echo ""
echo "=== 恢复完成 ==="
echo "如果浏览器仍无法访问，请尝试: 关闭浏览器重新打开，或切换一次 Wi-Fi 开关"
