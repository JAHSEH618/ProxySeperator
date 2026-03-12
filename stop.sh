#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_DIR="$ROOT_DIR/.run"

FRONTEND_PID_FILE="$RUN_DIR/frontend-watch.pid"
BACKEND_PID_FILE="$RUN_DIR/backend.pid"

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

stop_process() {
  local name="$1"
  local pid_file="$2"
  local pid

  pid="$(read_pid "$pid_file")"
  if [[ -z "$pid" ]]; then
    echo "$name 未运行。"
    rm -f "$pid_file"
    return
  fi

  if ! is_running "$pid"; then
    echo "$name 的 PID 文件已过期，清理完成。"
    rm -f "$pid_file"
    return
  fi

  echo "停止 $name (PID: $pid)..."
  kill "$pid" 2>/dev/null || true

  for _ in {1..20}; do
    if ! is_running "$pid"; then
      rm -f "$pid_file"
      echo "$name 已停止。"
      return
    fi
    sleep 0.25
  done

  echo "$name 未在预期时间内退出，强制结束。"
  kill -9 "$pid" 2>/dev/null || true
  rm -f "$pid_file"
}

stop_process "后端应用" "$BACKEND_PID_FILE"
stop_process "前端 watch" "$FRONTEND_PID_FILE"

echo "停止完成。"
