import React, { useEffect, useRef } from "react";
import { Box, Text, useBoxMetrics } from "ink";
import { theme } from "./theme.js";

interface Props {
  currentSessionId?: string;
  onHeightChange?: (h: number) => void;
}

// 顶部状态栏：当前会话信息（速度、id）。会话切换通过 Ctrl+X l/r 的会话对话框完成。
export function StatusBar({ currentSessionId, onHeightChange }: Props) {
  const barRef = useRef(null);
  const { height: barH, hasMeasured } = useBoxMetrics(barRef);

  useEffect(() => {
    if (hasMeasured && barH > 0) onHeightChange?.(barH);
  }, [barH, hasMeasured, onHeightChange]);

  return (
    <Box ref={barRef} flexDirection="row" gap={1} flexShrink={0} paddingLeft={1} paddingRight={1}>
      <Text color={theme.text} bold>
        TARS
      </Text>
      {currentSessionId ? (
        <>
          <Text color={theme.textMuted}>·</Text>
          <Text color={theme.accent}>session {currentSessionId.slice(0, 8)}</Text>
          <Text color={theme.textMuted}>·</Text>
          <Text color={theme.dim}>Ctrl+X l/r 切换</Text>
        </>
      ) : null}
    </Box>
  );
}