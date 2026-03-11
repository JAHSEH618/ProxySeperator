#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_DIR="$ROOT_DIR/.run"
BIN_DIR="$RUN_DIR/bin"
LOG_DIR="$RUN_DIR/logs"

FRONTEND_PID_FILE="$RUN_DIR/frontend-watch.pid"
BACKEND_PID_FILE="$RUN_DIR/backend.pid"
FRONTEND_LOG="$LOG_DIR/frontend-watch.log"
BACKEND_LOG="$LOG_DIR/backend.log"
BACKEND_BIN="$BIN_DIR/proxyseparator-dev"

read_pid() {
  local pid_file="$1"
  if [[ -f "$pid_file" ]]; then
    tr -d '[:space:]' < "$pid_file"
  fi
}

is_running() {
  local pid="${1:-}"
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

ensure_stopped() {
  local name="$1"
  local pid_file="$2"
  local pid

  pid="$(read_pid "$pid_file")"
  if is_running "$pid"; then
    echo "$name 已在运行，先执行 ./stop.sh 再重新启动。"
    echo "PID: $pid"
    exit 1
  fi
  rm -f "$pid_file"
}

timestamp() {
  date "+%Y-%m-%d %H:%M:%S"
}

mkdir -p "$BIN_DIR" "$LOG_DIR"

ensure_stopped "前端 watch" "$FRONTEND_PID_FILE"
ensure_stopped "后端应用" "$BACKEND_PID_FILE"

if [[ ! -d "$ROOT_DIR/frontend/node_modules" ]]; then
  echo "缺少前端依赖目录: $ROOT_DIR/frontend/node_modules"
  echo "先执行: cd frontend && npm install"
  exit 1
fi

echo "[$(timestamp)] 预构建前端资源..." | tee "$FRONTEND_LOG"
(
  cd "$ROOT_DIR/frontend"
  npm run build >> "$FRONTEND_LOG" 2>&1
)

echo "[$(timestamp)] 启动前端构建监听..." | tee -a "$FRONTEND_LOG"
(
  cd "$ROOT_DIR/frontend"
  nohup npm run build -- --watch >> "$FRONTEND_LOG" 2>&1 &
  echo $! > "$FRONTEND_PID_FILE"
)

echo "[$(timestamp)] 编译后端应用..." | tee "$BACKEND_LOG"
(
  cd "$ROOT_DIR"
  go build -o "$BACKEND_BIN" ./cmd/proxyseparator >> "$BACKEND_LOG" 2>&1
)

echo "[$(timestamp)] 启动后端应用..." | tee -a "$BACKEND_LOG"
(
  cd "$ROOT_DIR"
  nohup "$BACKEND_BIN" >> "$BACKEND_LOG" 2>&1 &
  echo $! > "$BACKEND_PID_FILE"
)

sleep 1

FRONTEND_PID="$(read_pid "$FRONTEND_PID_FILE")"
BACKEND_PID="$(read_pid "$BACKEND_PID_FILE")"

if ! is_running "$FRONTEND_PID"; then
  echo "前端构建监听启动失败，查看日志: $FRONTEND_LOG"
  exit 1
fi

if ! is_running "$BACKEND_PID"; then
  echo "后端应用启动失败，查看日志: $BACKEND_LOG"
  exit 1
fi

cat <<EOF
启动完成
- 前端 watch PID: $FRONTEND_PID
- 后端应用 PID: $BACKEND_PID
- 前端日志: $FRONTEND_LOG
- 后端日志: $BACKEND_LOG

说明:
- 前端会持续构建到 frontend/dist
- 后端会从 frontend/dist 读取资源
- 修改前端后，如界面未自动刷新，重新打开应用或执行 ./restart.sh
EOF
