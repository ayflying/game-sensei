#!/usr/bin/env bash
# 启动项目自带的 Ollama 实例（模型库放在项目内，不依赖任何远程设备）。
#
#   bash tools/serve_ollama.sh
#
# 与系统安装的 Ollama 服务隔离开：独立端口 11435 + 独立模型目录 .ollama/models。
# 关闭：Ctrl+C 或 kill 该进程；模型文件在 .ollama/（已 gitignore，不入库）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${OLLAMA_PORT:-11435}"

# 定位 ollama 可执行文件：优先 PATH，其次 Windows 默认安装目录
if command -v ollama >/dev/null 2>&1; then
  OLLAMA_BIN="$(command -v ollama)"
elif [ -x "$LOCALAPPDATA/Programs/Ollama/ollama.exe" ]; then
  OLLAMA_BIN="$LOCALAPPDATA/Programs/Ollama/ollama.exe"
elif [ -x "C:/Users/$USER/AppData/Local/Programs/Ollama/ollama.exe" ]; then
  OLLAMA_BIN="C:/Users/$USER/AppData/Local/Programs/Ollama/ollama.exe"
else
  echo "找不到 ollama 可执行文件，请先安装：https://ollama.com/download" >&2
  exit 1
fi

mkdir -p "$ROOT/.ollama/models"

export OLLAMA_MODELS="$ROOT/.ollama/models"
export OLLAMA_HOST="127.0.0.1:$PORT"
export OLLAMA_KEEP_ALIVE="${OLLAMA_KEEP_ALIVE:-30m}"

# 拉模型走代理（本机直连 registry.ollama.ai 不稳定；不需要时置空即可）
export HTTPS_PROXY="${HTTPS_PROXY:-http://127.0.0.1:10808}"
export HTTP_PROXY="${HTTP_PROXY:-http://127.0.0.1:10808}"

echo "ollama: $OLLAMA_BIN"
echo "模型库: $OLLAMA_MODELS"
echo "监听:   http://$OLLAMA_HOST"
echo

exec "$OLLAMA_BIN" serve
