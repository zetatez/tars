import React from "react";
import { Box, Text } from "ink";
import type { Message, ToolCall } from "../types.js";
import { describeTool, formatArgv, type ToolLine } from "../format.js";
import { Markdown } from "./markdown.js";
import { theme } from "./theme.js";

// 工具输出自动折叠的行数上限
const COLLAPSE_LINES = 10;

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

function ToolPanel({ t, expanded }: { t: ToolCall; expanded: boolean }) {
  const d = describeTool(t);
  const running = d.state === "other";
  const title = toolTitle(t);
  const details = t.name === "exec_command" ? d.lines.filter((l) => l.kind !== "cmd") : d.lines;
  const st = toolStateIcon(t);
  const bg = toolStateBg(t);

  // 扁平化为单行，过长自动折叠
  const flat: ToolLine[] = [];
  for (const l of details) {
    for (const s of l.text.split("\n")) {
      flat.push({ kind: l.kind, text: s });
    }
  }
  const collapsed = !expanded && flat.length > COLLAPSE_LINES;
  const shown = collapsed ? flat.slice(0, COLLAPSE_LINES) : flat;
  const hidden = flat.length - shown.length;

  return (
    <Box flexDirection="column" marginTop={1} backgroundColor={bg} paddingX={1} paddingY={1}>
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
      {flat.length > COLLAPSE_LINES ? (
        <Text color={theme.textMuted}>
          {"… " + (expanded ? `已展开（${flat.length} 行）` : `${hidden} more lines`)}
          {expanded ? " · ctrl+o 折叠" : " · ctrl+o 展开"}
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
  return (
    <Box flexDirection="column" flexShrink={0} marginTop={1} width="100%" backgroundColor={theme.userMessageBg} paddingX={1} paddingY={1}>
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
function AssistantMessageView({ m, toolsExpanded }: { m: Message; toolsExpanded: boolean }) {
  const c = m.content;
  if (c.error) {
    return (
      <Box flexDirection="column" flexShrink={0} marginTop={1} width="100%" backgroundColor={theme.toolErrorBg} paddingX={1} paddingY={1}>
        <Text color={theme.error}>{c.error}</Text>
      </Box>
    );
  }
  const tools = c.tools ?? [];
  return (
    <Box flexDirection="column" flexShrink={0} marginTop={1} width="100%" paddingLeft={1} paddingRight={1}>
      {tools.map((t, i) => (
        <ToolPanel key={t.id || i} t={t} expanded={toolsExpanded} />
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
  toolsExpanded,
}: {
  m: Message;
  model: string;
  last: boolean;
  running: boolean;
  elapsed: number;
  toolsExpanded: boolean;
}) {
  void model;
  void last;
  void running;
  void elapsed;
  if (m.role === "user") return <UserMessageView m={m} />;
  return <AssistantMessageView m={m} toolsExpanded={toolsExpanded} />;
}
