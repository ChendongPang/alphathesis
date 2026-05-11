#!/usr/bin/env bash
set -euo pipefail

# =========================
# QuantAgent local vLLM launcher
# Default: disable Qwen3 thinking
# =========================

IMAGE="${IMAGE:-vllm/vllm-openai:latest}"
HF_CACHE="${HF_CACHE:-$HOME/.cache/huggingface}"
DTYPE="${DTYPE:-half}"

# =========================
# Chat model (Qwen3)
# GPU_MEMORY_UTILIZATION 从 0.85 降到 0.80，为 embedding 模型让出显存
# =========================
CONTAINER_NAME="${CONTAINER_NAME:-quantagent-vllm}"
MODEL="${MODEL:-Qwen/Qwen3-8B}"
PORT="${PORT:-8000}"
GPU_MEMORY_UTILIZATION="${GPU_MEMORY_UTILIZATION:-0.80}"
MAX_MODEL_LEN="${MAX_MODEL_LEN:-4096}"

# 关键默认项：Qwen3 关闭 thinking
REASONING_PARSER="${REASONING_PARSER:-qwen3}"
DEFAULT_CHAT_TEMPLATE_KWARGS="${DEFAULT_CHAT_TEMPLATE_KWARGS:-{\"enable_thinking\": false}}"

# Tool calling: ThesisParserAgent needs OpenAI-compatible tool_calls.
# For Qwen/Qwen3-8B, hermes is the practical default in vLLM.
ENABLE_AUTO_TOOL_CHOICE="${ENABLE_AUTO_TOOL_CHOICE:-true}"
TOOL_CALL_PARSER="${TOOL_CALL_PARSER:-hermes}"

# 额外参数，可按需覆盖
EXTRA_ARGS="${EXTRA_ARGS:-}"

# =========================
# Embedding model (Qwen3-Embedding)
# 4090 24GB: chat 占 0.80 (19.2GB) + embed 占 0.15 (3.6GB) = 22.8GB，留 1.2GB buffer
# max_model_len=512 (chunks≈200 tokens)，大幅减少 KV cache 预分配
# =========================
EMBED_CONTAINER_NAME="${EMBED_CONTAINER_NAME:-quantagent-embedding}"
EMBED_MODEL="${EMBED_MODEL:-Qwen/Qwen3-Embedding-0.6B}"
EMBED_PORT="${EMBED_PORT:-8001}"
EMBED_GPU_MEMORY_UTILIZATION="${EMBED_GPU_MEMORY_UTILIZATION:-0.15}"
EMBED_MAX_MODEL_LEN="${EMBED_MAX_MODEL_LEN:-512}"

usage() {
  cat <<EOF
Usage:
  $0 start            启动 chat 模型 (Qwen3)
  $0 stop             停止 chat 容器
  $0 restart          重启 chat 容器
  $0 logs             查看 chat 日志
  $0 status           查看容器状态
  $0 test             测试 /v1/chat/completions
  $0 test-tools       测试 OpenAI tool calling 配置
  $0 models           测试 /v1/models

  $0 start-embed      启动 embedding 模型 (Qwen3-Embedding, port 8001)
  $0 stop-embed       停止 embedding 容器
  $0 restart-embed    重启 embedding 容器
  $0 logs-embed       查看 embedding 日志
  $0 test-embed       测试 /v1/embeddings

Chat model env vars:
  MODEL=Qwen/Qwen3-8B
  PORT=8000
  CONTAINER_NAME=quantagent-vllm
  GPU_MEMORY_UTILIZATION=0.80
  MAX_MODEL_LEN=4096
  DTYPE=half
  REASONING_PARSER=qwen3
  DEFAULT_CHAT_TEMPLATE_KWARGS='{"enable_thinking": false}'
  ENABLE_AUTO_TOOL_CHOICE=true
  TOOL_CALL_PARSER=hermes
  EXTRA_ARGS="--enforce-eager"

Embedding model env vars:
  EMBED_MODEL=Qwen/Qwen3-Embedding
  EMBED_PORT=8001
  EMBED_CONTAINER_NAME=quantagent-embedding
  EMBED_GPU_MEMORY_UTILIZATION=0.10
  EMBED_MAX_MODEL_LEN=8192

  IMAGE=vllm/vllm-openai:latest    (shared)
  HF_CACHE=\$HOME/.cache/huggingface  (shared)

Examples:
  $0 start && $0 start-embed      同时启动 chat + embedding
  MODEL=Qwen/Qwen3-14B $0 restart
  DEFAULT_CHAT_TEMPLATE_KWARGS='{"enable_thinking": true}' $0 restart
  EMBED_MODEL=BAAI/bge-m3 $0 start-embed
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Error: command not found: $1" >&2
    exit 1
  }
}

check_prereqs() {
  require_cmd docker
  require_cmd curl
  require_cmd mkdir

  echo "[info] checking docker..."
  docker version >/dev/null

  echo "[info] checking GPU access in WSL..."
  if command -v nvidia-smi >/dev/null 2>&1; then
    nvidia-smi >/dev/null || {
      echo "[error] nvidia-smi failed. GPU may not be available in WSL." >&2
      exit 1
    }
  else
    echo "[warn] nvidia-smi not found in current shell; continuing."
  fi

  echo "[info] checking docker GPU..."
  docker run --rm --gpus all nvidia/cuda:12.4.0-runtime-ubuntu22.04 nvidia-smi >/dev/null 2>&1 || {
    cat >&2 <<EOF
[error] Docker GPU test failed.
Please check:
  1. Docker Desktop is running
  2. WSL integration is enabled
  3. Docker GPU support is working
EOF
    exit 1
  }
}

stop_container() {
  if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then
    echo "[info] stopping container: $CONTAINER_NAME"
    docker rm -f "$CONTAINER_NAME" >/dev/null
  else
    echo "[info] container not found: $CONTAINER_NAME"
  fi
}

start_container() {
  check_prereqs
  mkdir -p "$HF_CACHE"

  if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then
    echo "[info] removing existing container: $CONTAINER_NAME"
    docker rm -f "$CONTAINER_NAME" >/dev/null
  fi

  echo "[info] starting vLLM..."
  echo "  IMAGE=$IMAGE"
  echo "  MODEL=$MODEL"
  echo "  PORT=$PORT"
  echo "  HF_CACHE=$HF_CACHE"
  echo "  GPU_MEMORY_UTILIZATION=$GPU_MEMORY_UTILIZATION"
  echo "  MAX_MODEL_LEN=$MAX_MODEL_LEN"
  echo "  DTYPE=$DTYPE"
  echo "  REASONING_PARSER=$REASONING_PARSER"
  echo "  DEFAULT_CHAT_TEMPLATE_KWARGS=$DEFAULT_CHAT_TEMPLATE_KWARGS"
  echo "  ENABLE_AUTO_TOOL_CHOICE=$ENABLE_AUTO_TOOL_CHOICE"
  echo "  TOOL_CALL_PARSER=$TOOL_CALL_PARSER"
  echo "  EXTRA_ARGS=$EXTRA_ARGS"

  local tool_args=()
  if [[ "$ENABLE_AUTO_TOOL_CHOICE" == "true" ]]; then
    tool_args+=(--enable-auto-tool-choice)
    tool_args+=(--tool-call-parser "$TOOL_CALL_PARSER")
  fi

  docker run -d \
    --name "$CONTAINER_NAME" \
    --gpus all \
    --ipc=host \
    -p "${PORT}:8000" \
    -v "${HF_CACHE}:/root/.cache/huggingface" \
    "$IMAGE" \
    "$MODEL" \
    --dtype "$DTYPE" \
    --gpu-memory-utilization "$GPU_MEMORY_UTILIZATION" \
    --max-model-len "$MAX_MODEL_LEN" \
    --reasoning-parser "$REASONING_PARSER" \
    --default-chat-template-kwargs "$DEFAULT_CHAT_TEMPLATE_KWARGS" \
    "${tool_args[@]}" \
    ${EXTRA_ARGS}

  echo "[info] container started: $CONTAINER_NAME"
  echo "[info] waiting for API to become ready..."

  local retries=90
  local i
  for ((i=1; i<=retries; i++)); do
    if curl -fsS "http://localhost:${PORT}/v1/models" >/dev/null 2>&1; then
      echo "[ok] vLLM is ready at http://localhost:${PORT}"
      return 0
    fi
    sleep 2
  done

  echo "[warn] vLLM not ready yet. Check logs with:"
  echo "  $0 logs"
}

show_logs() {
  docker logs -f "$CONTAINER_NAME"
}

show_status() {
  docker ps -a --filter "name=^${CONTAINER_NAME}$"
  docker ps -a --filter "name=^${EMBED_CONTAINER_NAME}$"
}

# =========================
# Embedding model commands
# =========================

start_embed_container() {
  check_prereqs
  mkdir -p "$HF_CACHE"

  if docker ps -a --format '{{.Names}}' | grep -qx "$EMBED_CONTAINER_NAME"; then
    echo "[info] removing existing container: $EMBED_CONTAINER_NAME"
    docker rm -f "$EMBED_CONTAINER_NAME" >/dev/null
  fi

  echo "[info] starting embedding model..."
  echo "  IMAGE=$IMAGE"
  echo "  EMBED_MODEL=$EMBED_MODEL"
  echo "  EMBED_PORT=$EMBED_PORT"
  echo "  EMBED_GPU_MEMORY_UTILIZATION=$EMBED_GPU_MEMORY_UTILIZATION"
  echo "  EMBED_MAX_MODEL_LEN=$EMBED_MAX_MODEL_LEN"
  echo "  DTYPE=$DTYPE"

  docker run -d \
    --name "$EMBED_CONTAINER_NAME" \
    --gpus all \
    --ipc=host \
    -p "${EMBED_PORT}:8000" \
    -v "${HF_CACHE}:/root/.cache/huggingface" \
    "$IMAGE" \
    "$EMBED_MODEL" \
    --dtype "$DTYPE" \
    --gpu-memory-utilization "$EMBED_GPU_MEMORY_UTILIZATION" \
    --max-model-len "$EMBED_MAX_MODEL_LEN"

  echo "[info] container started: $EMBED_CONTAINER_NAME"
  echo "[info] waiting for embedding API to become ready..."

  local retries=90
  local i
  for ((i=1; i<=retries; i++)); do
    if curl -fsS "http://localhost:${EMBED_PORT}/v1/models" >/dev/null 2>&1; then
      echo "[ok] embedding model is ready at http://localhost:${EMBED_PORT}"
      return 0
    fi
    sleep 2
  done

  echo "[warn] embedding model not ready yet. Check logs with:"
  echo "  $0 logs-embed"
}

stop_embed_container() {
  if docker ps -a --format '{{.Names}}' | grep -qx "$EMBED_CONTAINER_NAME"; then
    echo "[info] stopping container: $EMBED_CONTAINER_NAME"
    docker rm -f "$EMBED_CONTAINER_NAME" >/dev/null
  else
    echo "[info] container not found: $EMBED_CONTAINER_NAME"
  fi
}

show_embed_logs() {
  docker logs -f "$EMBED_CONTAINER_NAME"
}

test_embed() {
  curl -fsS "http://localhost:${EMBED_PORT}/v1/embeddings" \
    -H "Content-Type: application/json" \
    -d "{
      \"model\": \"${EMBED_MODEL}\",
      \"input\": [\"Apple iPhone revenue grew 6% year-over-year.\", \"苹果公司营收同比增长6%。\"]
    }" | python3 -m json.tool || \
  curl -fsS "http://localhost:${EMBED_PORT}/v1/embeddings" \
    -H "Content-Type: application/json" \
    -d "{
      \"model\": \"${EMBED_MODEL}\",
      \"input\": [\"Apple iPhone revenue grew 6% year-over-year.\", \"苹果公司营收同比增长6%。\"]
    }"
}

test_models() {
  curl -fsS "http://localhost:${PORT}/v1/models" | python3 -m json.tool || \
  curl -fsS "http://localhost:${PORT}/v1/models"
}

test_chat() {
  curl -fsS "http://localhost:${PORT}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{
      \"model\": \"${MODEL}\",
      \"messages\": [
        {\"role\": \"system\", \"content\": \"你是一个简洁助手。请用一句话直接给出最终答案。\"},
        {\"role\": \"user\", \"content\": \"一句话解释什么是RAG。\"}
      ],
      \"temperature\": 0.2,
      \"max_tokens\": 128
    }" | python3 -m json.tool || \
  curl -fsS "http://localhost:${PORT}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{
      \"model\": \"${MODEL}\",
      \"messages\": [
        {\"role\": \"system\", \"content\": \"你是一个简洁助手。请用一句话直接给出最终答案。\"},
        {\"role\": \"user\", \"content\": \"一句话解释什么是RAG。\"}
      ],
      \"temperature\": 0.2,
      \"max_tokens\": 128
    }"
}

test_tool_calling() {
  curl -fsS "http://localhost:${PORT}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{
      \"model\": \"${MODEL}\",
      \"messages\": [
        {\"role\": \"system\", \"content\": \"你是工具调用测试助手。如果需要查询股票代码，请调用工具。\"},
        {\"role\": \"user\", \"content\": \"帮我查询茅台的股票代码。\"}
      ],
      \"tools\": [
        {
          \"type\": \"function\",
          \"function\": {
            \"name\": \"resolve_symbol\",
            \"description\": \"Resolve a public company name into candidate stock symbols.\",
            \"parameters\": {
              \"type\": \"object\",
              \"properties\": {
                \"query\": {\"type\": \"string\", \"description\": \"Company name or ticker hint\"}
              },
              \"required\": [\"query\"],
              \"additionalProperties\": false
            }
          }
        }
      ],
      \"tool_choice\": \"auto\",
      \"temperature\": 0.1,
      \"max_tokens\": 256
    }" | python3 -m json.tool || \
  curl -fsS "http://localhost:${PORT}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{
      \"model\": \"${MODEL}\",
      \"messages\": [
        {\"role\": \"system\", \"content\": \"你是工具调用测试助手。如果需要查询股票代码，请调用工具。\"},
        {\"role\": \"user\", \"content\": \"帮我查询茅台的股票代码。\"}
      ],
      \"tools\": [
        {
          \"type\": \"function\",
          \"function\": {
            \"name\": \"resolve_symbol\",
            \"description\": \"Resolve a public company name into candidate stock symbols.\",
            \"parameters\": {
              \"type\": \"object\",
              \"properties\": {
                \"query\": {\"type\": \"string\", \"description\": \"Company name or ticker hint\"}
              },
              \"required\": [\"query\"],
              \"additionalProperties\": false
            }
          }
        }
      ],
      \"tool_choice\": \"auto\",
      \"temperature\": 0.1,
      \"max_tokens\": 256
    }"
}

main() {
  local cmd="${1:-}"
  case "$cmd" in
    start)
      start_container
      ;;
    stop)
      stop_container
      ;;
    restart)
      stop_container
      start_container
      ;;
    logs)
      show_logs
      ;;
    status)
      show_status
      ;;
    test)
      test_chat
      ;;
    test-tools)
      test_tool_calling
      ;;
    models)
      test_models
      ;;
    start-embed)
      start_embed_container
      ;;
    stop-embed)
      stop_embed_container
      ;;
    restart-embed)
      stop_embed_container
      start_embed_container
      ;;
    logs-embed)
      show_embed_logs
      ;;
    test-embed)
      test_embed
      ;;
    *)
      usage
      exit 1
      ;;
  esac
}

main "${1:-}"
