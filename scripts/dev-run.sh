#!/usr/bin/env bash
# PassPal 本地调试启动脚本（Git Bash / WSL）
#
# 作用：读取项目根目录的 .env，逐行注入进程环境后启动服务。
#
# 为什么不直接 `source .env`：
#   ADMIN_PASSWORD_HASH 里含有 $ 字符（$argon2id$v=19$...），
#   交给 shell 展开会被吃掉，导致启动时 hash 校验失败。
#   这里只做字符串切片，不做任何展开。

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT/.env"

if [ ! -f "$ENV_FILE" ]; then
  echo "缺少 $ENV_FILE" >&2
  echo "先执行：cp .env.example .env 并填写 ADMIN_PASSWORD_HASH 与 DATA_ENCRYPTION_KEY_V1" >&2
  exit 1
fi

while IFS= read -r line || [ -n "$line" ]; do
  # 跳过空行与注释
  case "$line" in
    '' | '#'*) continue ;;
  esac
  case "$line" in
    *=*) ;;
    *) continue ;;
  esac

  key="${line%%=*}"
  value="${line#*=}"

  # 去掉首尾空白（仅 key 侧，value 只去包裹的单引号）
  key="$(printf '%s' "$key" | tr -d '[:space:]')"
  value="${value%\'}"
  value="${value#\'}"

  [ -z "$key" ] && continue
  export "$key=$value"
done < "$ENV_FILE"

# 选择可执行文件
if [ -f "$ROOT/passpal.exe" ]; then
  BIN="$ROOT/passpal.exe"
elif [ -f "$ROOT/passpal" ]; then
  BIN="$ROOT/passpal"
else
  echo "找不到 passpal 可执行文件，请先执行：make build" >&2
  exit 1
fi

cd "$ROOT"
echo "PassPal 启动中：APP_ENV=$APP_ENV  ${BIND_ADDR:-127.0.0.1}:${PORT:-3000}"
exec "$BIN" serve
