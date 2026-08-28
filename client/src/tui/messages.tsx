import React from "react";
import { Box, Text, useStdout, measureElement, render, type DOMElement } from "ink";
import { Writable } from "node:stream";
import type { Message, ToolCall } from "../types.js";
import { describeTool, formatArgv, formatMsgTime, type ToolLine } from "../format.js";
import { Markdown } from "./markdown.js";
import { theme, charWidth } from "./theme.js";

// 离屏测量消息实际渲染行数（不显示到终端）。用真实组件渲染、measureElement 取自然高度，
// 返回 seq → 内容行数（不含 marginTop）。用于行级滚动窗口计算，避免裁剪盒内测量被约束。
// columns = 终端总列数（供 useStdout().columns 匹配屏幕宽度），消息区宽度取 columns-4。
export function measureMessages(
  msgs: Message[],
  columns: number,
  expandedTools: ReadonlySet<string>,
): Promise<Map<number, number>> {
  return new Promise((resolve) => {
    if (msgs.length === 0) return resolve(new Map());
    // 屏幕消息区宽度：外层 padding(2+2) + 右侧滚动条(1)。滚动条显示时消息实际宽 columns-5。
    const msgWidth = Math.max(20, columns - 5);
    const out = new Writable({
      write(_c: Buffer, _e: unknown, cb: () => void) {
        cb();
      },
    }) as unknown as NodeJS.WriteStream;
    (out as { columns?: number }).columns = columns;
    (out as { rows?: number }).rows = 30;
    const heights = new Map<number, number>();
    let pending = msgs.length;
    let app: { unmount: () => void } | null = null;
    const finish = () => {
      if (--pending === 0) {
        app?.unmount();
        resolve(heights);
      }
    };
    app = render(
      <Box flexDirection="column" width={msgWidth}>
        {msgs.map((m) => (
          <Box
            key={m.seq}
            ref={(el) => {
              if (el) {
                heights.set(m.seq, measureElement(el).height);
                finish();
              }
            }}
          >
            <MessageView
              m={m}
              model=""
              last={false}
              running={false}
              elapsed={0}
              expandedTools={expandedTools}
              toggleTool={() => {}}
              registerTool={() => {}}
            />
          </Box>
        ))}
      </Box>,
      { stdout: out },
    );
  });
}

// 工具输出自动折叠：超过 COLLAPSE_LINES 行，或渲染后（含长行换行）超过 COLLAPSE_RENDER_LINES 行即折叠
const COLLAPSE_LINES = 10;
const COLLAPSE_RENDER_LINES = 12;
// 折叠状态下单行文本的显示上限（避免超长单行占满屏幕）
const LINE_TEXT_LIMIT = 200;

// 估算一段文本按给定宽度渲染（wrap 换行）后的行数
function estWrapLines(text: string, width: number): number {
  if (!text) return 0;
  let n = 0;
  for (const raw of text.split("\n")) {
    if (/^\s*$/.test(raw)) {
      n += 1;
      continue;
    }
    n += Math.max(1, Math.ceil(charWidth(raw.trim()) / Math.max(1, width)));
  }
  return n;
}

// 截断超长行：折叠时单行超过上限加 "…" 提示
function truncateLine(text: string, limit: number): string {
  if (charWidth(text) <= limit) return text;
  return text.slice(0, limit - 1).replace(/\s+$/, "") + "…";
}

// 工具面板在屏幕上的区域（供鼠标点击命中）
export interface ToolRegion {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface ToolPanelProps {
  t: ToolCall;
  expanded: boolean;
  onToggle: () => void;
  register?: (id: string, region: ToolRegion | null) => void;
}

// 工具标题（`$ cmd`、`◈ "query"`、`→ Read path` 等）
function toolTitle(t: ToolCall): string {
  const args = (t.args ?? {}) as Record<string, unknown>;
  const s = (v: unknown): string | undefined => (typeof v === "string" ? v : undefined);
  switch (t.name) {
    case "exec_command":
      return Array.isArray(args.argv) ? formatArgv(args.argv as string[]) : t.name;
    case "webfetch":
      return s(args.url) ?? t.name;
    case "websearch":
      return `"${s(args.query) ?? ""}"`;
    case "read_file":
    case "write_file":
    case "edit_file":
      return s(args.filePath) ?? s(args.path) ?? t.name;
    case "glob":
    case "grep":
      return `"${s(args.pattern) ?? ""}"`;
    case "memory_query":
      return `"${s(args.query) ?? s(args.key) ?? ""}"`;
    default:
      return t.name;
  }
}

// 状态图标与颜色：成功✓绿 / 失败✗红 / 运行中▶
function toolStateIcon(t: ToolCall): { icon: string; color: string } {
  const d = describeTool(t);
  if (d.state === "error" || d.state === "rejected") return { icon: "✗", color: theme.error };
  if (d.state === "ok") return { icon: "✓", color: theme.success };
  return { icon: "▶", color: theme.accent };
}

function toolStateBg(t: ToolCall): string {
  const d = describeTool(t);
  if (d.state === "error" || d.state === "rejected") return theme.toolErrorBg;
  return theme.toolSuccessBg;
}

function ToolPanel({ t, expanded, onToggle, register }: ToolPanelProps) {
  const { stdout } = useStdout();
  const cols = stdout.columns ?? 80;
  const contentW = Math.max(20, cols - 10); // 面板 paddingX 各 1 + 图标占位
  const ref = React.useRef<DOMElement | null>(null);

  const d = describeTool(t);
  const running = d.state === "other";
  const title = toolTitle(t);
  const details = t.name === "exec_command" ? d.lines.filter((l) => l.kind !== "cmd") : d.lines;
  const st = toolStateIcon(t);
  const bg = toolStateBg(t);

  // 扁平化为单行
  const flat: ToolLine[] = [];
  for (const l of details) {
    for (const s of l.text.split("\n")) {
      flat.push({ kind: l.kind, text: s });
    }
  }

  // 折叠判定：总行数或渲染后总行数（长行换行）超过上限
  const totalRender = flat.reduce((a, l) => a + estWrapLines(l.text, contentW), 0);
  const isFoldable = flat.length > COLLAPSE_LINES || totalRender > COLLAPSE_RENDER_LINES;
  const collapsed = !expanded && isFoldable;

  // 折叠时保留前 K 行（按渲染行数预算），并截断超长行
  let shown = flat;
  let hidden = 0;
  if (collapsed) {
    let used = 0;
    let k = 0;
    for (; k < flat.length; k++) {
      const add = estWrapLines(flat[k].text, contentW);
      if (k > 0 && used + add > COLLAPSE_RENDER_LINES) break;
      used += add;
    }
    shown = flat.slice(0, Math.max(1, k)).map((l) => ({
      kind: l.kind,
      text: truncateLine(l.text, LINE_TEXT_LIMIT),
    }));
    hidden = flat.length - shown.length;
  }

  // 可折叠面板无论当前折叠还是展开都注册区域，点击可来回切换（展开/收起）。
  // 不可折叠（内容短）不注册，避免无意义点击。
  React.useEffect(() => {
    if (!register) return;
    if (isFoldable && ref.current) {
      const m = measureElement(ref.current);
      register(t.id, { x: m.x, y: m.y, width: m.width, height: m.height });
    } else {
      register(t.id, null);
    }
    return () => {
      register(t.id, null);
    };
  }, [isFoldable, collapsed, register, t.id]);

  return (
    <Box ref={ref} flexDirection="column" marginTop={1} backgroundColor={bg} paddingX={1} paddingY={1}>
      <Box flexDirection="row">
        <Box width={2}>
          <Text color={st.color}>{running ? "▶" : st.icon}</Text>
        </Box>
        <Text color={theme.toolTitle}>{title}</Text>
        {running ? <Text color={theme.toolTitle}> …</Text> : null}
      </Box>
      {shown.map((l, i) => (
        <Text key={i} wrap="wrap" color={lineColor(l)}>
          {l.text.trim()}
        </Text>
      ))}
      {collapsed ? (
        <Text color={theme.textMuted} wrap="wrap">
          {"… " + `已折叠（${hidden} 行隐藏） · 点击此面板展开`}
        </Text>
      ) : null}
      {d.state === "error" || d.state === "rejected" ? (
        <Text color={theme.error}>✗ {d.state}</Text>
      ) : null}
    </Box>
  );
}

function lineColor(l: ToolLine): string {
  switch (l.kind) {
    case "cmd":
      return theme.info;
    case "err":
      return theme.error;
    case "link":
      return theme.markdownLink;
    case "info":
      return theme.success; // exit 0 / count 等成功信息用绿色
    default:
      return theme.text; // 输出结果用亮色，避免与背景相近的灰
  }
}

// 用户消息：整行 #343541 背景面板（pi userMessageBg），内部 markdown + padding
function UserMessageView({ m }: { m: Message }) {
  const files = m.content.files ?? [];
  const t = formatMsgTime(m.created);
  return (
    <Box flexDirection="column" flexShrink={0} marginTop={1} width="100%" backgroundColor={theme.userMessageBg} paddingX={1} paddingY={1}>
      {t ? (
        <Box paddingBottom={1}>
          <Text color={theme.textMuted}>{t}</Text>
        </Box>
      ) : null}
      <Markdown content={m.content.text ?? ""} />
      {files.length > 0 ? (
        <Box flexDirection="row" paddingTop={1} gap={1} flexWrap="wrap">
          {files.map((f, i) => (
            <Text key={i} color={theme.userMessageText}>
              {f}
            </Text>
          ))}
        </Box>
      ) : null}
    </Box>
  );
}

// 助手消息：纯 markdown 无背景，outputPad=1 左缩进；工具调用为块状面板（可折叠）
function AssistantMessageView({ m, expandedTools, toggleTool, registerTool }: {
  m: Message;
  expandedTools: ReadonlySet<string>;
  toggleTool: (id: string) => void;
  registerTool: (id: string, region: ToolRegion | null) => void;
}) {
  const c = m.content;
  if (c.error) {
    return (
      <Box flexDirection="column" flexShrink={0} marginTop={1} width="100%" backgroundColor={theme.toolErrorBg} paddingX={1} paddingY={1}>
        <Text color={theme.error} wrap="wrap">
          ✗ {c.error}
        </Text>
      </Box>
    );
  }
  const tools = c.tools ?? [];
  const time = formatMsgTime(m.created);
  return (
    <Box flexDirection="column" flexShrink={0} marginTop={1} width="100%" paddingLeft={1} paddingRight={1}>
      {time ? (
        <Box paddingBottom={tools.length > 0 ? 0 : 1}>
          <Text color={theme.textMuted}>{time}</Text>
        </Box>
      ) : null}
      {tools.map((t, i) => (
        <ToolPanel
          key={t.id || i}
          t={t}
          expanded={expandedTools.has(t.id)}
          onToggle={() => toggleTool(t.id)}
          register={registerTool}
        />
      ))}
      {c.text ? (
        <Box flexShrink={0}>
          <Markdown content={c.text} />
        </Box>
      ) : null}
    </Box>
  );
}

export function MessageView({
  m,
  model,
  last,
  running,
  elapsed,
  expandedTools,
  toggleTool,
  registerTool,
}: {
  m: Message;
  model: string;
  last: boolean;
  running: boolean;
  elapsed: number;
  expandedTools: ReadonlySet<string>;
  toggleTool: (id: string) => void;
  registerTool: (id: string, region: ToolRegion | null) => void;
}) {
  void model;
  void last;
  void running;
  void elapsed;
  if (m.role === "user") return <UserMessageView m={m} />;
  return (
    <AssistantMessageView
      m={m}
      expandedTools={expandedTools}
      toggleTool={toggleTool}
      registerTool={registerTool}
    />
  );
}
