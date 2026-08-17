// pi agent 主题（../pi/packages/coding-agent/src/modes/interactive/theme/dark.json）
export const theme = {
  // 核心
  text: "#d4d4d4",
  textMuted: "#808080",
  dim: "#666666",
  darkGray: "#505050",
  accent: "#8abeb7",
  primary: "#8abeb7",
  secondary: "#5f87ff",
  info: "#5f87ff",
  success: "#b5bd68",
  error: "#cc6666",
  warning: "#ffff00",
  // 背景
  background: "#0f0f12",
  backgroundPanel: "#101014",
  backgroundElement: "#18181e",
  // 边框
  border: "#5f87ff",
  borderMuted: "#505050",
  borderActive: "#00d7ff",
  // 用户消息 / 工具状态面板
  userMessageBg: "#343541",
  userMessageText: "#d4d4d4",
  toolPendingBg: "#1e1e24",
  toolSuccessBg: "#1e1e24",
  toolErrorBg: "#2a2020",
  toolTitle: "#d4d4d4",
  toolOutput: "#d4d4d4",
  // markdown
  markdownHeading: "#f0c674",
  markdownLink: "#81a2be",
  markdownCode: "#8abeb7",
  markdownCodeBlock: "#b5bd68",
  markdownQuote: "#808080",
  markdownListBullet: "#8abeb7",
  codeBlockBg: "#18181e",
  // 输入区（兼容旧引用，统一为暗色 + 边框）
  inputBg: "#101014",
  inputText: "#d4d4d4",
  inputPlaceholder: "#808080",
  inputModeBuild: "#5f87ff",
  inputModePlan: "#8abeb7",
  agentBg: "#18181e",
} as const;

// 取某个字符串的显示宽度（含 CJK）
export function charWidth(s: string): number {
  let w = 0;
  for (const ch of s) {
    const code = ch.codePointAt(0) ?? 0;
    const wide =
      code >= 0x1100 && (code <= 0x115f ||
        (code >= 0x2e80 && code <= 0xa4cf) ||
        (code >= 0xac00 && code <= 0xd7a3) ||
        (code >= 0xf900 && code <= 0xfaff) ||
        (code >= 0xfe30 && code <= 0xfe4f) ||
        (code >= 0xff00 && code <= 0xff60) ||
        (code >= 0xffe0 && code <= 0xffe6) ||
        (code >= 0x20000 && code <= 0x2fffd) ||
        (code >= 0x30000 && code <= 0x3fffd));
    w += wide ? 2 : 1;
  }
  return w;
}

// 将单行文本按显示宽度硬切（单词可能被切断，用于无法换行的场景）
export function wrapHard(text: string, width: number): string[] {
  if (width <= 0) return text ? [text] : [];
  const out: string[] = [];
  for (const para of text.split("\n")) {
    if (para === "") {
      out.push("");
      continue;
    }
    let rest = para;
    while (charWidth(rest) > width) {
      let take = 0;
      let tw = 0;
      for (const ch of rest) {
        const cw = charWidth(ch);
        if (tw + cw > width) break;
        take++;
        tw += cw;
      }
      out.push(rest.slice(0, take));
      rest = rest.slice(take);
    }
    out.push(rest);
  }
  return out;
}
