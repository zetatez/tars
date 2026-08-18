import React from "react";
import { Box, Text } from "ink";
import { theme } from "./theme.js";

// 欢迎页 "TARS" 像素 logo：5 行 × 每字母 5 列，两段式（左半 muted、右半亮色）
const LOGO = [
  "▄▄▄▄▄  ▄█▄  ▄▄▄▄▄  ▄▄▄▄▄",
  " ▀█▀   ▀█▀  ▐█▀█▌  ▐█▀▀▀",
  " ▀█▀  ▐█▄█▌ ▐█▄█▌  ▐█▄▄▄",
  " ▀█▀  █▀▀▀█ ▐█▀▀▀  ▀▀▀▀█",
  " ▀▀▀  ▀▀▀▀▀ ▀▀▀▀▀  ▀▀▀▀▀",
];
// 左半 "TA"（muted）与右半 "RS"（亮色）的分界：T(5)+空格(1)+A(5)=11
const MID = 11;

export function Logo() {
  return (
    <Box flexDirection="column">
      {LOGO.map((row, i) => (
        <Box key={i} flexDirection="row" gap={0}>
          <Text color={theme.textMuted}>{row.slice(0, MID)}</Text>
          <Text color={theme.text} bold>
            {row.slice(MID)}
          </Text>
        </Box>
      ))}
    </Box>
  );
}
