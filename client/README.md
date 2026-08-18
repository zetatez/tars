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
# 无命令参数：默认进入交互式 TUI（需要 TTY）
node dist/main.js --base-url http://127.0.0.1:8899 --key <API_KEY>

# 命令行模式（单独调用各子命令）
node dist/main.js --key <API_KEY> health
node dist/main.js --key <API_KEY> session create
node dist/main.js --key <API_KEY> prompt <sessionId> "帮我检查磁盘空间"
node dist/main.js --key <API_KEY> tui          # 显式进入 TUI
```

> 提示：将 `node dist/main.js` 设为别名，或 `npm link` 后用 `tars-cli` 命令。启动脚本 `tars-cli.sh` 无参数同样默认进入 TUI。

## 交互式 TUI

`tui`（别名 `ui`）命令启动全屏对话界面，布局与行为对齐 opencode（无边框、消息左边框强调、粘滞滚动）：

```
Tars  a1b2c3d4-…-完整session-id              192.168.1.10 idle
   ⚠ 再按一次 Esc 确认中断当前任务       （运行中：turn/审批/警告显示在此行）
│ 帮我检查磁盘空间
│ ✓ exec_command
│   $ df -h /tmp
│   exit 0
│   文件系统 容量 已用 可用 ...
│ ✓ websearch
│   query: tars agent
│   · Tars — GitHub
│     https://github.com/x/tars
│ 磁盘使用率 45% ...                           │   ← 右侧滚动条
↑ 已上滚 8 条 · PageDown 回到底部
> ▌
Enter 发送 · Esc 中断 · ↑↓ 历史 · PageUp/PageDown 滚动 · Ctrl-C 退出
```

- 自动创建新会话（或 `tui --session <id>` 进入已有会话）；新会话显示 `（新会话）`
- **布局**：无边框分区；标题 `Tars` + 完整 session id 居左，服务器 IP 与运行状态（含耗时）靠右；turn id / 审批 / 警告显示在状态下方独立行；用户消息青色左边框、assistant 回复绿色左边框、工具调用以 `✓`/`✗`/`△`/`▶` 图标标识
- **输入光标**：输入框末尾有闪烁块状光标（`▌`），无占位文字
- **滚动（粘滞，同 opencode）**：`PageUp`/`PageDown` 按半页（8 条）滚动；在底部=跟随最新消息，一旦上滚窗口底部即冻结，**turn 运行中新消息到达不会移动已滚动的窗口**；`PageDown` 滚回底部即恢复跟随；右侧滚动条随滚动位置移动
- **流式**：对话与工具调用（含具体命令 `$`、退出码、搜索结果链接）实时渲染，彩色区分
- **Esc 两次中断**：运行中第一次按 Esc 显示警告「再按一次 Esc 确认中断当前任务」，2 秒内再按才真正中断（防误触）
- **多轮安全**：快 LLM 时通过轮询会话状态兜底，turn 结束后状态可靠回到 `idle`
- 快捷键：`Enter` 发送、`Esc` 中断（两次确认）、`↑`/`↓` 输入历史、`PageUp`/`PageDown` 滚动消息、`Ctrl-C` 退出（运行中先中断）

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

发送后订阅 SSE 实时显示：用户输入、工具调用（含参数与结果）、agent 回复、审批请求；`turn.done` 或会话状态变为空闲时自动结束。支持幂等（自动生成 `Idempotency-Key`）。

输出格式（带颜色；`$` 为执行的具体命令，`exit` 为退出码，链接为搜索结果 URL）：

```
turn: <id>
[user] 帮我看看磁盘
[exec_command ok]
  $ df -h /tmp
  exit 0
  文件系统 容量 已用 可用 ...
[websearch ok]
  query: tars agent golang
  · Tars — GitHub
    https://github.com/x/tars
[webfetch ok]
  GET https://example.com/docs
  HTTP 200
  ...
[assistant] ...
(done in 1.2s)
```

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
