import React, { useCallback, useEffect, useRef, useState } from "react";
import { Box, Text, useBoxMetrics, useInput } from "ink";
import type { API, GlobalSessionEntry } from "../api.js";
import { theme } from "./theme.js";

const PAGE = 8;

interface Props {
  api: API;
  currentSessionId?: string;
  onHeightChange?: (h: number) => void;
}

// 顶部状态栏：TARS 标题 + 全局活跃会话列表（top 8、可翻页）。
// 本 client 创建的会话标注 RW，其它标注 RO；Ctrl+PageDown/PageUp 翻页。
export function StatusBar({ api, currentSessionId, onHeightChange }: Props) {
  const [sessions, setSessions] = useState<GlobalSessionEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [err, setErr] = useState<string>();
  const barRef = useRef(null);
  const { height: barH, hasMeasured } = useBoxMetrics(barRef);

  useEffect(() => {
    if (hasMeasured && barH > 0) onHeightChange?.(barH);
  }, [barH, hasMeasured, onHeightChange]);

  const load = useCallback(
    (off: number) => {
      void api
        .globalSessions(PAGE, off)
        .then((r) => {
          setSessions(r.sessions);
          setTotal(r.total);
          setErr(undefined);
        })
        .catch((e: Error) => setErr(e.message));
    },
    [api],
  );

  useEffect(() => {
    load(offset);
    const t = setInterval(() => load(offset), 15000);
    return () => clearInterval(t);
  }, [load, offset]);

  useInput((_input, key) => {
    if (key.pageDown && key.ctrl) {
      setOffset((o) => (o + PAGE < total ? o + PAGE : o));
    } else if (key.pageUp && key.ctrl) {
      setOffset((o) => Math.max(0, o - PAGE));
    }
  });

  const page = Math.floor(offset / PAGE) + 1;
  const totalPages = Math.max(1, Math.ceil(total / PAGE));

  return (
    <Box ref={barRef} flexDirection="column" flexShrink={0} paddingLeft={1} paddingRight={1}>
      <Box flexDirection="column">
        <Text color={theme.textMuted}>
          sessions: [page {page}/{totalPages} · Ctrl+PageDown/PageUp]
        </Text>
        {sessions.length === 0 ? (
          <Text color={theme.textMuted}>  暂无活跃会话{err ? ` · ${err}` : ""}</Text>
        ) : (
          sessions.map((s, i) => {
            const idx = offset + i + 1;
            const isCurrent = currentSessionId != null && s.id === currentSessionId;
            const isRW = s.access === "rw";
            const user = s.client_user || s.key_id?.slice(0, 8) || "?";
            const ip = s.client_ip || "";
            const statusColor =
              s.status === "running" ? theme.accent : s.status === "failed" ? theme.error : theme.textMuted;
            return (
              <Box key={s.id} flexDirection="row" gap={1}>
                <Text color={isCurrent ? theme.secondary : theme.textMuted}>
                  {isCurrent ? "▶ " : "   "}
                  {idx}.
                </Text>
                <Text color={statusColor}>{s.status}</Text>
                <Text color={theme.text}>·</Text>
                <Text color={isCurrent ? theme.secondary : theme.text}>{s.id.slice(0, 8)}</Text>
                <Text color={theme.text}>·</Text>
                <Text color={theme.textMuted}>
                  {user}@{ip}
                </Text>
                <Text color={theme.text}>·</Text>
                <Text color={isRW ? theme.success : theme.warning} bold>
                  {isRW ? "RW" : "RO"}
                </Text>
              </Box>
            );
          })
        )}
      </Box>
    </Box>
  );
}