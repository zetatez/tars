import React from "react";
import { Box, Text } from "ink";
import { theme, charWidth } from "./theme.js";

type InlineSeg = { text: string; bold?: boolean; code?: boolean; link?: boolean; dim?: boolean; strike?: boolean };

// 行内 markdown：**粗体** *斜体* `代码` [文本](url) ~~删除线~~
function parseInline(src: string): InlineSeg[] {
  const out: InlineSeg[] = [];
  let i = 0;
  const n = src.length;
  while (i < n) {
    if (src.startsWith("**", i)) {
      const j = src.indexOf("**", i + 2);
      if (j !== -1) {
        out.push({ text: src.slice(i + 2, j), bold: true });
        i = j + 2;
        continue;
      }
    }
    if (src.startsWith("~~", i)) {
      const j = src.indexOf("~~", i + 2);
      if (j !== -1) {
        out.push({ text: src.slice(i + 2, j), strike: true });
        i = j + 2;
        continue;
      }
    }
    if (src[i] === "*" || src[i] === "_") {
      const c = src[i];
      const j = src.indexOf(c, i + 1);
      if (j !== -1) {
        out.push({ text: src.slice(i + 1, j), dim: true });
        i = j + 1;
        continue;
      }
    }
    if (src[i] === "`") {
      const j = src.indexOf("`", i + 1);
      if (j !== -1) {
        out.push({ text: src.slice(i + 1, j), code: true });
        i = j + 1;
        continue;
      }
    }
    if (src[i] === "[" && (i === 0 || src[i - 1] !== "\\")) {
      const close = src.indexOf("](", i);
      if (close !== -1) {
        const end = src.indexOf(")", close + 2);
        if (end !== -1) {
          const label = src.slice(i + 1, close);
          const url = src.slice(close + 2, end);
          out.push({ text: label || url, link: true });
          i = end + 1;
          continue;
        }
      }
    }
    let j = i;
    while (j < n && !["*", "_", "`", "[", "~"].includes(src[j])) j++;
    if (j > i) {
      out.push({ text: src.slice(i, j) });
      i = j;
    } else {
      out.push({ text: src[i] });
      i++;
    }
  }
  return out;
}

function InlineText({ segs, wrap }: { segs: InlineSeg[]; wrap?: boolean }) {
  return (
    <Text wrap={wrap ? "wrap" : undefined}>
      {segs.map((s, i) => {
        if (s.code) return <Text key={i} color={theme.markdownCode}>{s.text}</Text>;
        if (s.link) return <Text key={i} color={theme.markdownLink}>{s.text}</Text>;
        if (s.dim) return <Text key={i} dimColor>{s.text}</Text>;
        if (s.strike) return <Text key={i} strikethrough>{s.text}</Text>;
        if (s.bold) return <Text key={i} bold>{s.text}</Text>;
        return <Text key={i}>{s.text}</Text>;
      })}
    </Text>
  );
}

function InlineRow({ src }: { src: string }) {
  const segs = parseInline(src.trimEnd());
  if (segs.length === 0) return null;
  return <InlineText segs={segs} wrap />;
}

// 管道表格：按列宽对齐渲染
function splitRow(line: string): string[] {
  return line.split("|").map((c) => c.trim()).filter((c, i, arr) => !(i === 0 && c === "") && !(i === arr.length - 1 && c === ""));
}

function TableBlock({ rows }: { rows: string[][] }) {
  const header = rows[0] ?? [];
  const body = rows.slice(1);
  const cols = Math.max(1, ...body.map((r) => r.length), header.length);
  const widths: number[] = [];
  for (let c = 0; c < cols; c++) {
    let w = 0;
    for (const r of [header, ...body]) {
      if (r[c]) w = Math.max(w, charWidth(r[c]));
    }
    widths.push(Math.min(w, 40));
  }
  const cell = (s: string, w: number, bold: boolean) => {
    const pad = Math.max(0, w - charWidth(s));
    return <Text bold={bold}>{s + " ".repeat(pad)}</Text>;
  };
  return (
    <Box flexDirection="column">
      <Box flexDirection="row">
        {header.map((h, c) => (
          <Text key={c} bold>
            {cell(h, widths[c], true)}
            {c < cols - 1 ? "  " : ""}
          </Text>
        ))}
      </Box>
      <Text color={theme.textMuted}>
        {widths.map((w, c) => (c === 0 ? "─".repeat(w) : "  " + "─".repeat(w))).join("")}
      </Text>
      {body.map((r, ri) => (
        <Box key={ri} flexDirection="row">
          {widths.map((w, c) => (
            <Text key={c} color={theme.text}>
              {cell(r[c] ?? "", w, false)}
              {c < cols - 1 ? "  " : ""}
            </Text>
          ))}
        </Box>
      ))}
    </Box>
  );
}

// markdown 渲染（无外部依赖）：块间留一行，代码块带背景边框，支持表格/嵌套列表/删除线
export function Markdown({ content }: { content: string }) {
  const lines = content.replace(/\r\n/g, "\n").split("\n");
  const blocks: React.ReactNode[] = [];
  let key = 0;
  let i = 0;
  const push = (node: React.ReactNode) => {
    blocks.push(<Box key={key++} marginTop={blocks.length === 0 ? 0 : 1} flexShrink={0}>{node}</Box>);
  };

  while (i < lines.length) {
    const line = lines[i];
    // 表格：| 开头 + 分隔行
    if (/^\s*\|/.test(line) && i + 1 < lines.length && /^\s*\|[\s:|-]+\|\s*$/.test(lines[i + 1])) {
      const rows: string[][] = [];
      rows.push(splitRow(line));
      i += 2; // 跳过表头与分隔行
      while (i < lines.length && /^\s*\|/.test(lines[i])) {
        rows.push(splitRow(lines[i]));
        i++;
      }
      push(<TableBlock rows={rows} />);
      continue;
    }
    // 代码块
    const fence = line.match(/^```(.*)$/);
    if (fence) {
      const lang = fence[1].trim();
      const code: string[] = [];
      i++;
      while (i < lines.length && !/^```/.test(lines[i])) {
        code.push(lines[i]);
        i++;
      }
      i++;
      push(
        <Box flexDirection="column" borderStyle="single" borderColor={theme.borderMuted} backgroundColor={theme.codeBlockBg} paddingX={1} paddingY={1}>
          {lang ? <Text color={theme.textMuted}>{lang}</Text> : null}
          <Text color={theme.markdownCodeBlock}>{code.join("\n")}</Text>
        </Box>,
      );
      continue;
    }
    if (/^\s*$/.test(line)) {
      i++;
      continue;
    }
    const heading = line.match(/^(#{1,6})\s+(.*)$/);
    if (heading) {
      push(
        <Text bold color={theme.markdownHeading} wrap="wrap">
          {heading[2]}
        </Text>,
      );
      i++;
      continue;
    }
    if (/^\s*(---+|\*\*\*+|___+)\s*$/.test(line)) {
      push(<Text color={theme.textMuted}>{"─".repeat(8)}</Text>);
      i++;
      continue;
    }
    const quote = line.match(/^\s*>\s?(.*)$/);
    if (quote) {
      const q: string[] = [];
      while (i < lines.length) {
        const m = lines[i].match(/^\s*>\s?(.*)$/);
        if (!m) break;
        q.push(m[1]);
        i++;
      }
      push(
        <Box flexDirection="row">
          <Text color={theme.markdownQuote}>▌ </Text>
          <Box flexShrink={1}>
            <InlineRow src={q.join(" ")} />
          </Box>
        </Box>,
      );
      continue;
    }
    // 列表（支持嵌套缩进）
    const ul = line.match(/^(\s*)([-*+])\s+(.*)$/);
    if (ul) {
      const items: { level: number; text: string; ordered: boolean; num: number }[] = [];
      const readList = () => {
        while (i < lines.length) {
          const m = lines[i].match(/^(\s*)([-*+])\s+(.*)$/);
          if (!m) break;
          items.push({ level: Math.floor(m[1].length / 2), text: m[3], ordered: false, num: 0 });
          i++;
        }
      };
      readList();
      push(
        <Box flexDirection="column">
          {items.map((it, k) => (
            <Box key={k} flexDirection="row" paddingLeft={it.level * 2}>
              <Text color={theme.markdownListBullet}>{"• "}</Text>
              <Box flexShrink={1}>
                <InlineRow src={it.text} />
              </Box>
            </Box>
          ))}
        </Box>,
      );
      continue;
    }
    const ol = line.match(/^(\s*)(\d+)[.)]\s+(.*)$/);
    if (ol) {
      const items: { level: number; text: string }[] = [];
      while (i < lines.length) {
        const m = lines[i].match(/^(\s*)(\d+)[.)]\s+(.*)$/);
        if (!m) break;
        items.push({ level: Math.floor(m[1].length / 2), text: m[3] });
        i++;
      }
      push(
        <Box flexDirection="column">
          {items.map((it, k) => (
            <Box key={k} flexDirection="row" paddingLeft={it.level * 2}>
              <Text color={theme.info}>{`${k + 1}. `}</Text>
              <Box flexShrink={1}>
                <InlineRow src={it.text} />
              </Box>
            </Box>
          ))}
        </Box>,
      );
      continue;
    }
    if (/^!\[/.test(line)) {
      i++;
      continue;
    }
    // 普通段落
    const para: string[] = [line];
    i++;
    while (i < lines.length && lines[i].trim() !== "" && !/^(#{1,6})\s/.test(lines[i]) && !/^```/.test(lines[i]) && !/^\s*\|/.test(lines[i]) && !/^(\s*)([-*+]|\d+[.)])\s/.test(lines[i]) && !/^\s*>\s?/.test(lines[i])) {
      para.push(lines[i]);
      i++;
    }
    push(<InlineRow src={para.join(" ")} />);
  }
  if (blocks.length === 0) return null;
  return (
    <Box flexDirection="column" flexShrink={0}>
      {blocks}
    </Box>
  );
}
