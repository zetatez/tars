import React, { useEffect, useRef } from "react";
import { Box, Text, useBoxMetrics } from "ink";
import { theme } from "./theme.js";

interface Props {
  onHeightChange?: (h: number) => void;
}

// 顶部状态栏：应用标题 + NORM 模式快捷操作提示。会话切换通过 NORM 模式 z 键的搜索面板完成。
export function StatusBar({ onHeightChange }: Props) {
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
    </Box>
  );
}