import React, { useEffect, useState } from "react";
import { Box, Text, useInput, useStdout } from "ink";
import { theme } from "./theme.js";
import { KEY_HELP } from "./keys.js";
import type { API, GlobalSessionEntry } from "../api.js";
import type { Message } from "../types.js";

export interface SelectOption {
  key: string;
  title: string;
  detail?: string;
  footer?: string;
}

function DialogFrame({ children, title, maxHeight }: { children: React.ReactNode; title?: string; maxHeight?: number }) {
  return (
    <Box position="absolute" top={0} left={0} right={0} bottom={0} flexDirection="column" backgroundColor={theme.background} justifyContent="center" alignItems="center">
      <Box flexDirection="column" width="80%" maxHeight={maxHeight} borderStyle="single" borderColor={theme.border}>
        {title ? (
          <Box paddingLeft={1} paddingRight={1} paddingTop={1}>
            <Text bold>{title}</Text>
          </Box>
        ) : null}
        {children}
      </Box>
    </Box>
  );
}

export interface LeaderItem {
  key: string;
  label: string;
  run: () => void;
}

// Leader 菜单面板：纯展示，选中项由调用方通过 focus 控制（键盘由上层 useInput 处理）。
export function LeaderMenu({ items, focus, onClose }: { items: LeaderItem[]; focus: number; onClose: () => void }) {
  void onClose;
  return (
    <Box flexDirection="column" marginBottom={1} width="100%">
      <Box flexDirection="row" flexWrap="wrap">
        {items.map((item, i) => (
          <Box
            key={item.key}
            flexShrink={0}
            backgroundColor={i === focus ? theme.primary : theme.backgroundElement}
            paddingX={1}
            marginRight={1}
            marginBottom={1}
          >
            <Text color={i === focus ? theme.background : theme.text}>
              {item.key}) {item.label}
            </Text>
          </Box>
        ))}
      </Box>
      <Text color={theme.textMuted}>按字母选择 · ↑↓ 移动 · Enter 确认 · Esc 关闭</Text>
    </Box>
  );
}

export function DialogSelect({
  title,
  options,
  onSelect,
  onCancel,
  footerHint,
}: {
  title: string;
  options: SelectOption[];
  onSelect: (o: SelectOption) => void;
  onCancel: () => void;
  footerHint?: string;
}) {
  const { stdout } = useStdout();
  const rows = stdout.rows ?? 24;
  const maxOpts = Math.max(4, rows - 6);
  const [sel, setSel] = useState(0);
  const selVal = Math.max(0, Math.min(sel, options.length - 1));
  const winStart = options.length <= maxOpts ? 0 : Math.max(0, Math.min(sel - Math.floor(maxOpts / 2), options.length - maxOpts));
  const win = options.slice(winStart, winStart + maxOpts);
  useEffect(() => setSel(0), [title]);

  useInput((_input, key) => {
    if (key.upArrow || (key.ctrl && _input === "p")) setSel((s) => Math.max(0, s - 1));
    else if (key.downArrow || (key.ctrl && _input === "n")) setSel((s) => Math.min(options.length - 1, s + 1));
    else if (key.pageUp) setSel((s) => Math.max(0, s - maxOpts));
    else if (key.pageDown) setSel((s) => Math.min(options.length - 1, s + maxOpts));
    else if (key.return) onSelect(options[selVal]);
    else if (key.escape || (key.ctrl && _input === "c")) onCancel();
  });

  return (
    <DialogFrame title={title}>
      <Box flexDirection="column" paddingTop={1} paddingBottom={1}>
        {win.map((o, i) => {
          const idx = winStart + i;
          const isSel = idx === selVal;
          return (
            <Box key={o.key} flexDirection="row" flexShrink={0} paddingLeft={1} paddingRight={1} backgroundColor={isSel ? theme.primary : undefined}>
              <Box flexShrink={0}>
                <Text color={isSel ? theme.background : theme.text}>{o.title}</Text>
              </Box>
              <Text color={isSel ? theme.background : theme.textMuted} wrap="truncate">
                {o.detail ? "  " + o.detail : ""}
              </Text>
            </Box>
          );
        })}
        {options.length > maxOpts ? (
          <Text color={theme.textMuted}>
            … {options.length - maxOpts} more · ↑↓ scroll
          </Text>
        ) : null}
      </Box>
      <Box paddingLeft={1} paddingRight={1} paddingBottom={1}>
        <Text color={theme.textMuted}>{footerHint ?? "↑↓ navigate · enter select · esc cancel"}</Text>
      </Box>
    </DialogFrame>
  );
}

export function DialogConfirm({
  title,
  message,
  onConfirm,
  onCancel,
  cancelLabel = "Cancel",
  confirmLabel = "Confirm",
}: {
  title: string;
  message: string;
  onConfirm: () => void;
  onCancel: () => void;
  cancelLabel?: string;
  confirmLabel?: string;
}) {
  useInput((_input, key) => {
    if (key.return) onConfirm();
    else if (key.escape || (key.ctrl && _input === "c")) onCancel();
  });
  return (
    <DialogFrame title={title}>
      <Box paddingLeft={2} paddingRight={2} paddingTop={1} paddingBottom={1}>
        <Text wrap="wrap">{message}</Text>
      </Box>
      <Box flexDirection="row" paddingLeft={1} paddingRight={1} paddingBottom={1} gap={2}>
        <Text color={theme.primary}>enter {confirmLabel}</Text>
        <Text color={theme.textMuted}>esc {cancelLabel}</Text>
      </Box>
    </DialogFrame>
  );
}

// 审批对话框：工具调用需要用户批准时弹出，approve/deny 发送决策
export function DialogApproval({
  action,
  resource,
  onApprove,
  onDeny,
}: {
  action: string;
  resource: string;
  onApprove: () => void;
  onDeny: () => void;
}) {
  useInput((_input, key) => {
    if (key.return || (key.ctrl && _input === "y") || _input === "a" || _input === "y") onApprove();
    else if (key.escape || (key.ctrl && _input === "c") || _input === "d" || _input === "n") onDeny();
  });
  return (
    <DialogFrame title="Permission required">
      <Box flexDirection="column" paddingLeft={2} paddingRight={2} paddingTop={1} paddingBottom={1}>
        <Box flexDirection="row" gap={1}>
          <Text color={theme.warning}>⚠</Text>
          <Text bold color={theme.text}>
            {action}
          </Text>
        </Box>
        <Text color={theme.textMuted} wrap="wrap">
          {resource}
        </Text>
      </Box>
      <Box flexDirection="row" paddingLeft={1} paddingRight={1} paddingBottom={1} gap={2}>
        <Text color={theme.success}>enter / a approve</Text>
        <Text color={theme.error}>esc / d deny</Text>
      </Box>
    </DialogFrame>
  );
}

export function DialogHelp({ onClose }: { onClose: () => void }) {
  const { stdout } = useStdout();
  const cols = stdout.columns ?? 80;
  const bodyRows = Math.max(8, (stdout.rows ?? 24) - 4);
  const [off, setOff] = useState(0);
  const [sel, setSel] = useState(0);

  const { rows, total } = React.useMemo(() => {
    const flat: { key: string; desc: string; keys: string }[] = [];
    for (const item of KEY_HELP) {
      flat.push({ key: `${item.group}::${item.keys.join(", ")}`, desc: item.desc, keys: item.keys.join(", ") });
    }
    return { rows: flat, total: flat.length };
  }, []);
  void total;
  const keyW = Math.min(Math.round(cols * 0.45), Math.max(...rows.map((r) => r.keys.length), 18));
  const bodyRows2 = Math.max(5, Math.min(bodyRows - 2, rows.length));

  const selRow = Math.max(0, Math.min(sel, rows.length - 1));
  const winStart = Math.max(0, Math.min(off, Math.max(0, rows.length - bodyRows2)));
  const win = rows.slice(winStart, winStart + bodyRows2);

  useInput((_input, key) => {
    if (key.escape || (key.ctrl && _input === "c") || _input === "q" || _input === "?") {
      onClose();
      return;
    }
    if (key.upArrow) {
      setSel((s) => Math.max(0, s - 1));
      setOff((o) => Math.max(0, o - 1));
    } else if (key.downArrow) {
      setSel((s) => Math.min(rows.length - 1, s + 1));
      setOff((o) => Math.min(Math.max(0, rows.length - bodyRows2), o + 1));
    } else if (key.pageUp) {
      setSel((s) => Math.max(0, s - bodyRows2));
      setOff((o) => Math.max(0, o - bodyRows2));
    } else if (key.pageDown) {
      setSel((s) => Math.min(rows.length - 1, s + bodyRows2));
      setOff((o) => Math.min(Math.max(0, rows.length - bodyRows2), o + bodyRows2));
    }
  });

  return (
    <DialogFrame title={`Help (↑↓ scroll · esc/q close)`} maxHeight={bodyRows2 + 2}>
      <Box flexDirection="column" paddingTop={1} paddingBottom={1}>
        {win.map((item, i) => {
          const rowIdx = winStart + i;
          const isSel = rowIdx === selRow;
          return (
            <Box key={item.key} flexDirection="row" flexShrink={0} paddingLeft={1} paddingRight={1} backgroundColor={isSel ? theme.primary : undefined}>
              <Box width={keyW} flexShrink={0}>
                <Text color={isSel ? theme.background : theme.primary} wrap="truncate">
                  {item.keys}
                </Text>
              </Box>
              <Text color={isSel ? theme.background : theme.textMuted} wrap="truncate">
                {item.desc}
              </Text>
            </Box>
          );
        })}
      </Box>
      <Box paddingLeft={1} paddingRight={1} paddingBottom={1}>
        <Text color={theme.textMuted}>
          {rows.length} shortcuts · esc/q close
        </Text>
      </Box>
    </DialogFrame>
  );
}

// fzf 式会话切换器：搜索过滤 + 列表中移动 + 预览选中会话的最新对话。
export function SessionSwitcher({
  api,
  currentId,
  onSelect,
  onCancel,
}: {
  api: API;
  currentId?: string;
  onSelect: (id: string) => void;
  onCancel: () => void;
}) {
  const { stdout } = useStdout();
  const rows = stdout.rows ?? 24;
  const cols = stdout.columns ?? 80;
  const maxOpts = Math.max(4, rows - 10);
  const [sessions, setSessions] = useState<GlobalSessionEntry[]>([]);
  const [query, setQuery] = useState("");
  const [sel, setSel] = useState(0);
  const [preview, setPreview] = useState<Message[]>([]);
  const [cur, setCur] = useState(currentId ?? "");
  const [focusInput, setFocusInput] = useState(true);

  useEffect(() => {
    api
      .globalSessions(100, 0)
      .then((r) => setSessions(r.sessions))
      .catch(() => {});
  }, [api]);

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return sessions;
    return sessions.filter(
      (s) =>
        s.id.toLowerCase().includes(q) ||
        (s.cwd ?? "").toLowerCase().includes(q) ||
        (s.status ?? "").toLowerCase().includes(q),
    );
  }, [sessions, query]);

  const selVal = Math.max(0, Math.min(sel, filtered.length - 1));
  const winStart = filtered.length <= maxOpts ? 0 : Math.max(0, Math.min(selVal - Math.floor(maxOpts / 2), filtered.length - maxOpts));
  const win = filtered.slice(winStart, winStart + maxOpts);

  useEffect(() => {
    const s = filtered[selVal];
    if (!s || s.id === cur) return;
    setCur(s.id);
    api
      .messages(s.id, 0, 20)
      .then((msgs) => setPreview(msgs.slice(-10)))
      .catch(() => setPreview([]));
  }, [filtered, selVal, api, cur]);

  useEffect(() => setSel(0), [query]);

  useInput((input, key) => {
    if (key.escape || (key.ctrl && input === "c")) {
      onCancel();
      return;
    }
    if (key.return) {
      const s = filtered[selVal];
      if (s) onSelect(s.id);
      return;
    }
    if (focusInput) {
      if (key.upArrow || (key.ctrl && input === "p")) {
        setSel((v) => Math.max(0, v - 1));
        return;
      }
      if (key.downArrow || (key.ctrl && input === "n") || key.tab) {
        setSel((v) => Math.min(filtered.length - 1, v + 1));
        return;
      }
      if (key.backspace) {
        setQuery((q) => q.slice(0, -1));
        return;
      }
      if (input && !key.ctrl && !key.meta) {
        setQuery((q) => q + input);
        return;
      }
      return;
    }
  });

  const previewLines = React.useMemo(() => {
    const lines: string[] = [];
    for (const m of preview) {
      const prefix = m.role === "assistant" ? "assistant" : m.role;
      const text = (m.content?.text ?? "").split("\n")[0];
      if (text) lines.push(`${prefix}: ${text}`);
    }
    return lines.slice(-8);
  }, [preview]);

  const previewWidth = Math.max(10, Math.round(cols * 0.55) - 2);

  return (
    <DialogFrame title="Switch session (type to search · ↑↓ move · enter switch · esc close)">
      <Box flexDirection="row" paddingLeft={1} paddingRight={1} paddingTop={1}>
        <Text color={theme.textMuted}>⏎ </Text>
        <Text color={theme.text} wrap="truncate" inverse={false}>
          {query || <Text color={theme.inputPlaceholder}>search…</Text>}
        </Text>
      </Box>
      <Box flexDirection="row">
        <Box flexDirection="column" width={Math.round(cols * 0.4)} flexShrink={0}>
          {win.map((s, i) => {
            const idx = winStart + i;
            const isSel = idx === selVal;
            return (
              <Box key={s.id} flexDirection="row" paddingLeft={1} paddingRight={1} backgroundColor={isSel ? theme.primary : undefined}>
                <Text color={isSel ? theme.background : theme.text} wrap="truncate">
                  {s.id.slice(0, 8)}
                </Text>
                <Text color={isSel ? theme.background : theme.textMuted} wrap="truncate">
                  {" "}
                  {s.status}
                  {s.cwd ? ` · ${s.cwd.split("/").pop()}` : ""}
                </Text>
              </Box>
            );
          })}
          {filtered.length > maxOpts ? (
            <Text color={theme.textMuted}>… {filtered.length - maxOpts} more</Text>
          ) : null}
        </Box>
        <Box flexDirection="column" paddingLeft={1} paddingRight={1} width={Math.round(cols * 0.55)} flexShrink={0}>
          <Text color={theme.textMuted}>— preview: {cur.slice(0, 8)} —</Text>
          {previewLines.length === 0 ? (
            <Text color={theme.dim}>（空会话）</Text>
          ) : (
            previewLines.map((line, i) => (
              <Text key={i} color={theme.textMuted} wrap="truncate">
                {line}
              </Text>
            ))
          )}
        </Box>
      </Box>
      <Box paddingLeft={1} paddingRight={1} paddingBottom={1}>
        <Text color={theme.textMuted}>type filters · enter switch · esc close</Text>
      </Box>
    </DialogFrame>
  );
}
