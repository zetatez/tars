# tars CLI

tars AI agent 的命令行客户端（TypeScript，前后端分离）。通过 REST + SSE 与 tars 服务端交互，支持流式对话、会话管理、密钥管理与治理操作。

## 安装

要求：Node.js ≥ 18（内置 `fetch`）。

```bash
cd client
npm install
npm run build      # 编译到 dist/
```

## 快速开始

```bash
# 全局参数：--base-url（默认 http://localhost:8899）、--key
# 或用环境变量 TARS_BASE_URL / TARS_API_KEY
node dist/main.js --base-url http://127.0.0.1:8899 --key <API_KEY> health

# 创建会话
node dist/main.js --key <API_KEY> session create

# 发送 prompt 并流式查看结果
node dist/main.js --key <API_KEY> prompt <sessionId> "帮我检查磁盘空间"
```

> 提示：将 `node dist/main.js` 设为别名，或 `npm link` 后用 `tars-cli` 命令。

## 命令参考

### 通用参数（放在命令前）

| 参数             | 说明                                     |
| ---              | ---                                      |
| `--base-url, -u` | 服务端地址，默认 `http://localhost:8899` |
| `--key, -k`      | API Key（也可用环境变量 `TARS_API_KEY`） |

### health / version

```bash
tars-cli health            # 健康检查
tars-cli version           # 服务端版本
```

### session（会话）

```bash
tars-cli session create [--cwd <dir>] [--model <model>] [--prompt-mode interrupt|queue]
tars-cli session list
tars-cli session show <sessionId>
tars-cli session delete <sessionId>
```

### prompt（发送并流式显示）

```bash
tars-cli prompt <sessionId> <text...>
```

发送后订阅 SSE 实时显示：用户输入、工具调用（含结果）、agent 回复、审批请求；`turn.done` 或会话状态变为空闲时自动结束。支持幂等（自动生成 `Idempotency-Key`）。

### messages（历史消息）

```bash
tars-cli messages <sessionId> [--after <seq>]
```

### event（实时事件流）

```bash
tars-cli event <sessionId>     # 订阅 SSE，Ctrl-C 退出
```

### interrupt / rollback

```bash
tars-cli interrupt <sessionId>    # 中断当前 turn
tars-cli rollback <sessionId>     # 回滚最近一次系统写（写前备份恢复）
```

### keys（密钥管理，需 admin）

```bash
tars-cli keys create [--label <label>]   # 创建 key，明文只显示一次
tars-cli keys revoke <keyId>             # 吊销
```

### config（per-key 配置）

```bash
tars-cli config <keyId> get
tars-cli config <keyId> set <k=v> [k=v ...]
```

### stats（用量统计）

```bash
tars-cli stats <keyId>
```

## 示例

```bash
# 1. 创建会话
SID=$(tars-cli session create --cwd /workspace | grep -o 'session .*' | cut -d' ' -f2)

# 2. 流式对话
tars-cli prompt "$SID" "列出当前目录文件并说明"

# 3. 查看完整历史
tars-cli messages "$SID"

# 4. 用普通用户 key 创建会话
USER_KEY=$(tars-cli keys create --label "dev" | grep -o 'key:    .*' | cut -c 9-)
tars-cli --key "$USER_KEY" session create
```

## 输出说明

- 角色前缀带颜色：`[user]` 青色、`[assistant]` 绿色、工具调用 `[工具名 ok]` 灰色、错误红色、审批黄色。
- 工具结果默认只显示 stdout（截断的完整内容在后端有记录，可用 `messages` 查看消息 JSON）。

## 与后端交互说明

CLI 通过标准 API 工作，可对接任何 tars 实例：

- REST：`/api/v1/session`、`/api/v1/session/:id/prompt`、`/api/v1/session/:id/messages` 等
- SSE：`/api/v1/session/:id/event?after=<seq>`（回放 + 实时）
- 认证：`Authorization: Bearer <key>`
- 写操作（prompt/interrupt/rollback）仅限会话创建者；读操作默认全 key 可见

> 注意：快 LLM 场景下 turn 可能在订阅建立前结束（`turn.done` 为 live 事件会丢失），CLI 通过轮询会话状态兜底，保证 prompt 命令始终正确结束。
