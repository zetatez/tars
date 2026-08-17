#!/usr/bin/env bash
# tars-cli.sh — 便捷启动 tars CLI
# 用法: ./tars-cli.sh [--base-url URL] [--key KEY] <命令> ...
# 开发阶段自动处理：
#   - client 未构建/源码更新 → 自动构建
#   - 后端未运行 → 自动构建并安装到 /opt/tars/ 后后台启动（PID 写 /opt/tars/tars.pid）
#   - 未提供 key → 自动获取服务器 machine-id，按与后端一致的规则派生 admin key
# API Key 优先级: --key 参数 > TARS_API_KEY 环境变量 > 自动派生 admin key
set -euo pipefail

SOURCE="${BASH_SOURCE[0]}"
while [ -L "$SOURCE" ]; do
  DIR="$(cd -P "$(dirname "$SOURCE")" >/dev/null 2>&1 && pwd)"
  SOURCE="$(readlink "$SOURCE")"
  [[ $SOURCE != /* ]] && SOURCE="$DIR/$SOURCE"
done
SCRIPT_DIR="$(cd -P "$(dirname "$SOURCE")" >/dev/null 2>&1 && pwd)"

# 定位 client 目录与仓库根（脚本可能在项目根或 client/ 内）
if [ -d "$SCRIPT_DIR/client" ]; then
  CLIENT_DIR="$SCRIPT_DIR/client"; REPO_ROOT="$SCRIPT_DIR"
elif [ -f "$SCRIPT_DIR/package.json" ]; then
  CLIENT_DIR="$SCRIPT_DIR"; REPO_ROOT="$(cd "$(dirname "$CLIENT_DIR")" >/dev/null 2>&1 && pwd)"
else
  echo "error: 找不到 client 目录" >&2; exit 1
fi

MAIN="$CLIENT_DIR/dist/main.js"
BASE_URL="http://localhost:8899"
for ((i = 0; i < $#; i++)); do
  if [ "${!i}" = "--base-url" ] && [ $((i + 1)) -lt $# ]; then
    j=$((i + 1)); BASE_URL="${!j}"
  fi
done

if [ ! -f "$MAIN" ] || [ "$CLIENT_DIR/src/main.ts" -nt "$MAIN" ]; then
  echo "正在构建 tars-cli ..." >&2
  (cd "$CLIENT_DIR" && npm run build)
fi

probe() {
  node -e "fetch('$BASE_URL/healthz').then(r=>r.ok?process.exit(0):process.exit(1)).catch(()=>process.exit(1))" 2>/dev/null
}

# 后端未运行 → 自动构建/安装/启动
if ! probe; then
  TARS_DIR="/opt/tars"; BIN="$TARS_DIR/bin/tars"
  if [ ! -f "$BIN" ] || [ -n "$(find "$REPO_ROOT/cmd" "$REPO_ROOT/internal" -name '*.go' -newer "$BIN" 2>/dev/null | head -1)" ]; then
    echo "正在构建 tars 后端 ..." >&2
    (cd "$REPO_ROOT" && go build -trimpath -o build/tars ./cmd/tars)
    sudo install -Dm755 "$REPO_ROOT/build/tars" "$BIN"
  fi
  sudo mkdir -p "$TARS_DIR/data" "$TARS_DIR/work"
  CFG="$TARS_DIR/config.yaml"
  if [ ! -f "$CFG" ]; then
    PORT="$(node -e "const u=new URL('$BASE_URL');process.stdout.write(u.port||(u.protocol==='https:'?'443':'80'))")"
    sudo cp "$REPO_ROOT/config.example.yaml" "$CFG"
    sudo sed -i "s|^listen:.*|listen: \":$PORT\"|" "$CFG"
  fi
  # 透传 provider 环境变量给 sudo 启动的后端（sudo 默认清空环境）
  ENV_ARGS=()
  for v in $(grep -oE '\$\{[A-Za-z_][A-Za-z0-9_]*\}' "$CFG" 2>/dev/null | tr -d '${}' | sort -u); do
    eval "val=\${$v-}"
    [ -n "$val" ] && ENV_ARGS+=("$v=$val")
  done
  if [ ! -f "$TARS_DIR/tars.pid" ] || ! kill -0 "$(cat "$TARS_DIR/tars.pid")" 2>/dev/null; then
    echo "后端未运行，正在自动启动 tars 服务（$BASE_URL）..." >&2
    sudo env "${ENV_ARGS[@]}" sh -c "nohup $BIN --config $CFG >>$TARS_DIR/tars.log 2>&1 & echo \$! >$TARS_DIR/tars.pid"
  fi
  for _ in $(seq 1 30); do probe && break; sleep 0.5; done
  if ! probe; then echo "error: 后端启动失败，查看 $TARS_DIR/tars.log" >&2; exit 1; fi
fi

# 未提供 key → 自动获取 machine-id 并派生 admin key（与后端规则一致）
HAS_KEY=0
for a in "$@"; do
  if [ "$a" = "--key" ] || [ "$a" = "-k" ]; then HAS_KEY=1; fi
done
if [ "$HAS_KEY" -eq 0 ] && [ -z "${TARS_API_KEY:-}" ]; then
  ADMIN_KEY="$(node -e "$(cat <<'EOF'
const crypto = require("node:crypto");
const base = process.argv[1];
fetch(base + "/api/v1/machine-id").then(async (r) => {
  const mid = (await r.json()).machine_id;
  const dom = "tars:admin-key:v2";
  let h = crypto.createHmac("sha256", dom).update(mid).digest(); // t0 = HMAC(domain, machine_id)
  const b = Buffer.alloc(4);
  for (let i = 1; i <= 4096; i++) {                              // 4096 轮迭代扩展
    b.writeUInt32LE(i);
    h = crypto.createHash("sha256").update(h).update(mid).update(b).digest();
  }
  const keyID = "tars-admin-" + crypto.createHmac("sha256", dom).update("keyid:" + mid).digest("hex").slice(0, 12);
  console.log(keyID + "_" + h.toString("hex"));
}).catch(() => process.exit(1));
EOF
)" "$BASE_URL")" || { echo "error: 自动获取 admin key 失败（$BASE_URL）" >&2; exit 1; }
  echo "自动使用 admin key: ${ADMIN_KEY:0:24}..." >&2
  export TARS_API_KEY="$ADMIN_KEY"
fi

exec node "$MAIN" "$@"
