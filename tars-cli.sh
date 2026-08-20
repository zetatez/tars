#!/usr/bin/env bash
# 便捷启动 tars CLI（后端未运行时自动构建并后台启动）
# 用法: ./tars-cli.sh [--base-url URL] [--key KEY] <命令> ...
# 说明: 自动定位 client 目录；dist 缺失或源码更新时自动构建；参数原样透传。
#       后端健康检查失败时，自动 go build 并安装到标准目录 /opt/tars/ 后后台启动
#       （PID 写入 /opt/tars/tars.pid，退出 CLI 不停止后端）。
#       写 /opt/tars/ 需要 sudo（与 `make install` 一致）。
#       API Key 优先级: --key 参数 > TARS_API_KEY 环境变量 > 下方 DEFAULT_KEY。
set -euo pipefail

# 默认 API Key（同时作为自动拉起后端的 admin_key，部署后替换为你自己的 key）
DEFAULT_KEY="11111111-2222-3333-4444-555555555555_19898e078f86aae066029b82d50d02d3e5acca10568868b20c1bc43f645f42ad"

# 解析脚本真实路径（支持符号链接）
SOURCE="${BASH_SOURCE[0]}"
while [ -L "$SOURCE" ]; do
  DIR="$(cd -P "$(dirname "$SOURCE")" >/dev/null 2>&1 && pwd)"
  SOURCE="$(readlink "$SOURCE")"
  [[ $SOURCE != /* ]] && SOURCE="$DIR/$SOURCE"
done
SCRIPT_DIR="$(cd -P "$(dirname "$SOURCE")" >/dev/null 2>&1 && pwd)"

# 定位 client 目录与仓库根：脚本可能在项目根 或 client/ 内
if [ -d "$SCRIPT_DIR/client" ]; then
  CLIENT_DIR="$SCRIPT_DIR/client"
  REPO_ROOT="$SCRIPT_DIR"
elif [ -f "$SCRIPT_DIR/package.json" ]; then
  CLIENT_DIR="$SCRIPT_DIR"
  REPO_ROOT="$(cd "$(dirname "$CLIENT_DIR")" >/dev/null 2>&1 && pwd)"
else
  echo "error: 找不到 client 目录（脚本应位于项目根或 client/ 内）" >&2
  exit 1
fi

MAIN="$CLIENT_DIR/dist/main.js"
BASE_URL="http://localhost:8899"
for ((i = 0; i < $#; i++)); do
  if [ "${!i}" = "--base-url" ] && [ $((i + 1)) -lt $# ]; then
    j=$((i + 1))
    BASE_URL="${!j}"
  fi
done

# 未构建，或 src 比构建产物新 → 自动构建 client
if [ ! -f "$MAIN" ] || [ "$CLIENT_DIR/src/main.ts" -nt "$MAIN" ]; then
  echo "正在构建 tars-cli ..." >&2
  (cd "$CLIENT_DIR" && npm run build)
fi

# 后端健康检查（用 node，CLI 运行时必然可用）
probe() {
  node -e "fetch('$BASE_URL/healthz').then(r=>r.ok?process.exit(0):process.exit(1)).catch(()=>process.exit(1))" 2>/dev/null
}

if ! probe; then
  TARS_DIR="/opt/tars"
  BIN="$TARS_DIR/bin/tars"
  NEED_BUILD=0
  [ -f "$BIN" ] || NEED_BUILD=1
  if [ "$NEED_BUILD" -eq 0 ] && [ -n "$(find "$REPO_ROOT/cmd" "$REPO_ROOT/internal" -name '*.go' -newer "$BIN" 2>/dev/null | head -1)" ]; then
    NEED_BUILD=1
  fi
  if [ "$NEED_BUILD" -eq 1 ]; then
    echo "正在构建 tars 后端 ..." >&2
    (cd "$REPO_ROOT" && go build -trimpath -o build/tars ./cmd/tars)
    echo "安装到 $BIN ..." >&2
    sudo install -Dm755 "$REPO_ROOT/build/tars" "$BIN"
  fi

  sudo mkdir -p "$TARS_DIR/data" "$TARS_DIR/work"
  CFG="$TARS_DIR/config.yaml"
  if [ ! -f "$CFG" ]; then
    PORT="$(node -e "const u=new URL('$BASE_URL');process.stdout.write(u.port||(u.protocol==='https:'?'443':'80'))")"
    sudo cp "$REPO_ROOT/config.example.yaml" "$CFG"
    sudo sed -i \
      -e "s|^listen:.*|listen: \":$PORT\"|" \
      -e "s|^admin_key:.*|admin_key: \"$DEFAULT_KEY\"|" \
      "$CFG"
  elif grep -q '^admin_key: "00000000-0000-0000-0000-000000000000_' "$CFG"; then
    # 占位 admin_key 时注入默认 key，保证脚本自动拉起后可直接访问
    sudo sed -i "s|^admin_key:.*|admin_key: \"$DEFAULT_KEY\"|" "$CFG"
  fi

  if [ -f "$TARS_DIR/tars.pid" ] && kill -0 "$(cat "$TARS_DIR/tars.pid")" 2>/dev/null; then
    :
  else
    echo "后端未运行，正在自动启动 tars 服务（$BASE_URL）..." >&2
    sudo sh -c "nohup $BIN --config $CFG >>$TARS_DIR/tars.log 2>&1 & echo \$! >$TARS_DIR/tars.pid"
  fi

  for _ in $(seq 1 30); do
    probe && break
    sleep 0.5
  done
  if ! probe; then
    echo "error: 后端启动失败，查看 $TARS_DIR/tars.log" >&2
    exit 1
  fi
fi

# 若未通过参数/环境变量提供 key，则用脚本内置的默认 key
HAS_KEY=0
for a in "$@"; do
  [ "$a" = "--key" ] || [ "$a" = "-k" ] && HAS_KEY=1
done
if [ "$HAS_KEY" -eq 0 ] && [ -z "${TARS_API_KEY:-}" ] && [ -n "$DEFAULT_KEY" ]; then
  export TARS_API_KEY="$DEFAULT_KEY"
fi

exec node "$MAIN" "$@"
