import React, { useEffect, useState } from "react";
import { Box, Text, useInput } from "ink";
import { theme } from "./theme.js";
import { KEY_HELP } from "./keys.js";

export interface SelectOption {
  key: string;
  title: string;
  detail?: string;
  footer?: string;
}

function DialogFrame({ children, title }: { children: React.ReactNode; title?: string }) {
  return (
    <Box position="absolute" top={0} left={0} right={0} bottom={0} justifyContent="center" alignItems="center">
      <Box flexDirection="column" borderStyle="single" borderColor={theme.border}>
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
  const [sel, setSel] = useState(0);
  const shown = options.slice(0, 12);
  useEffect(() => setSel(0), [title]);

  useInput((_input, key) => {
    if (key.upArrow || (key.ctrl && _input === "p")) setSel((s) => Math.max(0, s - 1));
    else if (key.downArrow || (key.ctrl && _input === "n")) setSel((s) => Math.min(shown.length - 1, s + 1));
    else if (key.pageUp) setSel((s) => Math.max(0, s - 8));
    else if (key.pageDown) setSel((s) => Math.min(shown.length - 1, s + 8));
    else if (key.return) onSelect(shown[sel]);
    else if (key.escape || (key.ctrl && _input === "c")) onCancel();
  });

  return (
    <DialogFrame title={title}>
      <Box flexDirection="column" paddingTop={1} paddingBottom={1}>
        {shown.map((o, i) => (
          <Box
            key={o.key}
            flexDirection="row"
            paddingLeft={1}
            paddingRight={1}
            backgroundColor={i === sel ? theme.primary : undefined}
          >
            <Box flexShrink={0}>
              <Text color={i === sel ? theme.background : theme.text}>{o.title}</Text>
            </Box>
            {o.detail ? (
              <Text color={i === sel ? theme.background : theme.textMuted} dimColor>
                {"  " + o.detail}
              </Text>
            ) : null}
          </Box>
        ))}
        {options.length > shown.length ? (
          <Text color={theme.textMuted}>… {options.length - shown.length} more</Text>
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

export function DialogHelp({ onClose }: { onClose: () => void }) {  useInput((_input, key) => {
    if (key.escape || (key.ctrl && _input === "c") || _input === "q" || _input === "?") onClose();
  });
  const groups = new Map<string, typeof KEY_HELP>();
  for (const item of KEY_HELP) {
    if (!groups.has(item.group)) groups.set(item.group, []);
    groups.get(item.group)!.push(item);
  }
  return (
    <DialogFrame title="Help">
      <Box flexDirection="column" paddingTop={1} paddingBottom={1}>
        {[...groups.entries()].map(([group, items]) => (
          <Box key={group} flexDirection="column" paddingLeft={1} paddingRight={1} paddingBottom={1}>
            <Text bold color={theme.accent}>
              {group}
            </Text>
            {items.map((item, i) => (
              <Box key={i} flexDirection="row" paddingLeft={1}>
                <Box width={22}>
                  <Text color={theme.primary}>{item.keys.join(", ")}</Text>
                </Box>
                <Text color={theme.textMuted}>{item.desc}</Text>
              </Box>
            ))}
          </Box>
        ))}
      </Box>
      <Box paddingLeft={1} paddingRight={1} paddingBottom={1}>
        <Text color={theme.textMuted}>esc / q close</Text>
      </Box>
    </DialogFrame>
  );
}
