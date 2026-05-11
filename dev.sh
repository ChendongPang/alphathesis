#!/usr/bin/env bash
# dev.sh — AlphaThesis local service manager
#
# Usage:
#   ./dev.sh start        build 前端 dist，并启动后端托管页面
#   ./dev.sh stop         停止所有服务
#   ./dev.sh status       查看所有服务状态
#   ./dev.sh restart      stop + start
#   ./dev.sh web          单独启动 Vite 开发服务器（热更新）
#   ./dev.sh build-web    build 前端到 web/dist
#   ./dev.sh reset-data   清理业务数据（保留 users 用户/密码）
#   ./dev.sh logs [svc]   实时查看日志（svc: akserver|yfinance|server|web|debug|llm，默认 server）

set -euo pipefail
cd "$(dirname "$0")"

if [[ -f ".env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source ".env"
  set +a
fi

# ── 配置 ───────────────────────────────────────────────────────
DATABASE_URL="${DATABASE_URL:-postgres://postgres:postgres@localhost:5433/alphathesis?sslmode=disable}"
PORT="${PORT:-8080}"
PYADAPTER_PORT="${PYADAPTER_PORT:-8811}"
YFINANCE_MCP_PORT="${YFINANCE_MCP_PORT:-8812}"
WEB_PORT="${WEB_PORT:-5173}"

RUN_DIR=".run"
LOG_DIR="logs"

PID_AKSERVER="$RUN_DIR/akserver.pid"
PID_YFINANCE="$RUN_DIR/yfinance_mcp.pid"
PID_SERVER="$RUN_DIR/server.pid"
PID_WEB="$RUN_DIR/web.pid"

LOG_AKSERVER="$LOG_DIR/akserver.log"
LOG_YFINANCE="$LOG_DIR/yfinance_mcp.log"
LOG_SERVER="$LOG_DIR/server.log"
LOG_WEB="$LOG_DIR/web.log"
LOG_LLM="$LOG_DIR/llm.log"
WEB_DIST_DIR="${WEB_DIST_DIR:-web/dist}"

ensure_runtime_dirs() {
  mkdir -p "$RUN_DIR" "$LOG_DIR"
}

# ── 颜色 ───────────────────────────────────────────────────────
GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}[ok]${NC}   $*"; }
fail() { echo -e "${RED}[fail]${NC} $*"; }
info() { echo -e "${YELLOW}[info]${NC} $*"; }

# ── PID 工具 ───────────────────────────────────────────────────
is_running() {
  local pid_file="$1"
  [[ -f "$pid_file" ]] && kill -0 "$(cat "$pid_file")" 2>/dev/null
}

stop_pid() {
  local pid_file="$1" name="$2" port="${3:-}"
  if is_running "$pid_file"; then
    kill "$(cat "$pid_file")" 2>/dev/null && ok "stopped $name"
  else
    info "$name not running"
  fi
  rm -f "$pid_file"
  # belt-and-suspenders: kill anything still holding the port
  if [[ -n "$port" ]]; then
    local stale
    stale=$(lsof -ti :"$port" 2>/dev/null || true)
    if [[ -n "$stale" ]]; then
      echo "$stale" | xargs kill -9 2>/dev/null || true
      info "killed stale process on :$port"
    fi
  fi
}

wait_port() {
  local port="$1" name="$2" retries=40
  for ((i=1; i<=retries; i++)); do
    if nc -z 127.0.0.1 "$port" 2>/dev/null; then
      ok "$name is up on :$port"
      return 0
    fi
    sleep 0.5
  done
  fail "$name did not come up on :$port within ${retries}s"
  return 1
}

wait_http() {
  local url="$1" name="$2" retries=30
  for ((i=1; i<=retries; i++)); do
    if curl -s --max-time 2 "$url" -o /dev/null 2>/dev/null; then
      ok "$name HTTP ready at $url"
      return 0
    fi
    sleep 1
  done
  fail "$name HTTP not ready at $url after ${retries}s"
  return 1
}

# ── 启动 ───────────────────────────────────────────────────────
build_web() {
  info "building frontend into $WEB_DIST_DIR ..."
  (cd web && npm run build)
  ok "frontend built"
}

start_akserver() {
  ensure_runtime_dirs
  if is_running "$PID_AKSERVER"; then
    info "akserver already running (pid $(cat $PID_AKSERVER))"
    return
  fi
  info "starting akserver on :$PYADAPTER_PORT ..."
  PYTHONUNBUFFERED=1 python3 scripts/akserver.py --port "$PYADAPTER_PORT" \
    >> "$LOG_AKSERVER" 2>&1 &
  echo $! > "$PID_AKSERVER"
  wait_port "$PYADAPTER_PORT" "akserver"
}

start_yfinance() {
  ensure_runtime_dirs
  if is_running "$PID_YFINANCE"; then
    info "yfinance MCP already running (pid $(cat $PID_YFINANCE))"
    return
  fi
  info "starting yfinance MCP on :$YFINANCE_MCP_PORT ..."
  PYTHONUNBUFFERED=1 YFINANCE_MCP_PORT="$YFINANCE_MCP_PORT" \
    python3 scripts/yfinance_mcp_server.py \
    >> "$LOG_YFINANCE" 2>&1 &
  echo $! > "$PID_YFINANCE"
  wait_port "$YFINANCE_MCP_PORT" "yfinance MCP"
  wait_http "http://localhost:$YFINANCE_MCP_PORT/mcp" "yfinance MCP"
}

start_server() {
  ensure_runtime_dirs
  if is_running "$PID_SERVER"; then
    info "Go server already running (pid $(cat $PID_SERVER))"
    return
  fi
  if [[ -z "$DATABASE_URL" ]]; then
    fail "DATABASE_URL is not set. Add it to .env or export it."
    exit 1
  fi
  info "building Go server ..."
  go build -buildvcs=false -o "$RUN_DIR/server" ./cmd/server/
  info "starting Go server on :$PORT ..."
  DATABASE_URL="$DATABASE_URL" \
  PORT="$PORT" \
  PYADAPTER_URL="http://localhost:$PYADAPTER_PORT" \
  YFINANCE_MCP_URL="http://localhost:$YFINANCE_MCP_PORT/mcp" \
  WEB_DIST_DIR="$WEB_DIST_DIR" \
  LOG_FILE="$LOG_DIR/alphathesis.log" \
  LLM_LOG_FILE="$LOG_LLM" \
    "$RUN_DIR/server" >> "$LOG_SERVER" 2>&1 &
  echo $! > "$PID_SERVER"
  wait_port "$PORT" "Go server"
}

start_web() {
  ensure_runtime_dirs
  if is_running "$PID_WEB"; then
    info "web already running (pid $(cat $PID_WEB))"
    return
  fi
  info "starting Vite dev server on :$WEB_PORT ..."
  (cd web && npm run dev) >> "$LOG_WEB" 2>&1 &
  echo $! > "$PID_WEB"
  wait_port "$WEB_PORT" "Vite"
}

cmd_start() {
  build_web
  start_akserver
  start_yfinance
  start_server
  echo ""
  ok "all services started"
  ok "frontend is served by Go server at http://localhost:$PORT/"
}

# ── 停止 ───────────────────────────────────────────────────────
cmd_stop() {
  stop_pid "$PID_WEB"       "Vite"         "$WEB_PORT"
  stop_pid "$PID_SERVER"    "Go server"    "$PORT"
  stop_pid "$PID_YFINANCE"  "yfinance MCP" "$YFINANCE_MCP_PORT"
  stop_pid "$PID_AKSERVER"  "akserver"     "$PYADAPTER_PORT"
}

# ── 状态 ───────────────────────────────────────────────────────
svc_status() {
  local pid_file="$1" name="$2" port="$3"
  if is_running "$pid_file"; then
    local pid
    pid=$(cat "$pid_file")
    printf "  %-18s ${GREEN}running${NC}  pid=%-6s :%-5s\n" "$name" "$pid" "$port"
  else
    printf "  %-18s ${RED}stopped${NC}\n" "$name"
  fi
}

cmd_status() {
  echo ""
  echo "── AlphaThesis services ──────────────────────────────"
  svc_status "$PID_AKSERVER" "akserver"       "$PYADAPTER_PORT"
  svc_status "$PID_YFINANCE" "yfinance MCP"   "$YFINANCE_MCP_PORT"
  svc_status "$PID_SERVER"   "Go server"      "$PORT"
  svc_status "$PID_WEB"      "Vite optional"  "$WEB_PORT"
  if [[ -f "$WEB_DIST_DIR/index.html" ]]; then
    printf "  %-18s ${GREEN}present${NC}  %s\n" "web dist" "$WEB_DIST_DIR"
  else
    printf "  %-18s ${RED}missing${NC}  %s\n" "web dist" "$WEB_DIST_DIR"
  fi
  echo "──────────────────────────────────────────────────────"
  echo ""
}

# ── 清理业务数据 ───────────────────────────────────────────────
cmd_reset_data() {
  if ! command -v psql >/dev/null 2>&1; then
    fail "psql is not installed or not in PATH"
    exit 1
  fi

  info "clearing business data from database; users/passwords will be kept"
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
TRUNCATE TABLE
  daily_reports,
  thesis_score_history,
  assumption_score_history,
  evidence_snippets,
  candidate_chunks,
  candidate_assumptions,
  relevant_candidates,
  job_candidates,
  job_runs,
  assumptions,
  theses
RESTART IDENTITY CASCADE;
SQL
  ok "business data cleared; users table preserved"
}

# ── 日志 ───────────────────────────────────────────────────────
cmd_logs() {
  ensure_runtime_dirs
  local svc="${1:-server}"
  case "$svc" in
    akserver)  tail -f "$LOG_AKSERVER" ;;
    yfinance)  tail -f "$LOG_YFINANCE" ;;
    server)    tail -f "$LOG_SERVER" ;;
    web)       tail -f "$LOG_WEB" ;;
    debug)     tail -f "$LOG_DIR/alphathesis.log" ;;
    llm)       tail -f "$LOG_LLM" ;;
    *)
      echo "unknown service: $svc"
      echo "choices: akserver | yfinance | server | web | debug | llm"
      exit 1
      ;;
  esac
}

# ── main ───────────────────────────────────────────────────────
usage() {
  grep '^#   ' "$0" | sed 's/^# /  /'
}

case "${1:-}" in
  start)        cmd_start ;;
  stop)         cmd_stop ;;
  restart)      cmd_stop; cmd_start ;;
  status)       cmd_status ;;
  web)          start_web ;;
  build-web)    build_web ;;
  reset-data)   cmd_reset_data ;;
  logs)         cmd_logs "${2:-server}" ;;
  *)            usage; exit 1 ;;
esac
