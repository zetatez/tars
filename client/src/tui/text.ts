import { charWidth } from "./theme.js";

// 视觉行：text 为原缓冲区中的一段（不含换行），start 为其在原串中的偏移
export interface WrappedLine {
  text: string;
  start: number;
}

// 按显示宽度做单词级换行，输出各视觉行在原串中的切片
export function wrapLines(text: string, width: number): WrappedLine[] {
  const out: WrappedLine[] = [];
  if (text === "") return [{ text: "", start: 0 }];
  if (width <= 0) return [{ text, start: 0 }];
  let i = 0;
  while (i < text.length) {
    let end = text.indexOf("\n", i);
    if (end === -1) end = text.length;
    const para = text.slice(i, end);
    if (para === "") {
      out.push({ text: "", start: i });
      i = end + 1;
      continue;
    }
    const words = para.split(/(\s+)/);
    let line = "";
    let lineStart = i;
    let pos = i;
    for (const word of words) {
      if (line === "") {
        line = word;
        pos += word.length;
        continue;
      }
      if (charWidth(line) + charWidth(word) > width) {
        out.push({ text: line, start: lineStart });
        lineStart = pos;
        line = /^\s+$/.test(word) ? "" : word;
        pos += word.length;
      } else {
        line += word;
        pos += word.length;
      }
    }
    if (line !== "" || para !== "") out.push({ text: line, start: lineStart });
    i = end + 1;
  }
  if (out.length === 0) out.push({ text: "", start: 0 });
  return out;
}

// 缓冲区偏移 → 视觉坐标
export function cursorPos(wrapped: WrappedLine[], offset: number): { line: number; col: number } {
  if (wrapped.length === 0) return { line: 0, col: 0 };
  for (let li = 0; li < wrapped.length; li++) {
    const wl = wrapped[li];
    const end = wl.start + wl.text.length;
    if (offset >= wl.start && offset <= end) {
      return { line: li, col: charWidth(wl.text.slice(0, offset - wl.start)) };
    }
  }
  const last = wrapped[wrapped.length - 1];
  return { line: wrapped.length - 1, col: charWidth(last.text) };
}

// 视觉列 → 缓冲区偏移（用于上下移动光标）
export function offsetAtCol(wl: WrappedLine, col: number): number {
  let w = 0;
  for (let k = 0; k < wl.text.length; k++) {
    if (w >= col) return wl.start + k;
    w += charWidth(wl.text[k]);
  }
  return wl.start + wl.text.length;
}

// 单行文本按单词切分（用于 alt+left/right 词移动、ctrl+w 删词）
export function wordStarts(text: string): number[] {
  const out: number[] = [];
  let prevSpace = true;
  for (let k = 0; k < text.length; k++) {
    const space = /\s/.test(text[k]);
    if (!space && prevSpace) out.push(k);
    prevSpace = space;
  }
  return out;
}

// 视觉行渲染分段：把 [selStart,selEnd) 与光标位置切成若干 {text, style} 段
export interface Segment {
  text: string;
  sel: boolean;
  cursor: boolean; // 光标块（仅当没有选择覆盖时）
}

export function lineSegments(
  wl: WrappedLine,
  selStart: number | undefined,
  selEnd: number | undefined,
  cursor: number | undefined,
): Segment[] {
  const localStart = selStart === undefined ? -1 : Math.max(0, selStart - wl.start);
  const localEnd = selEnd === undefined ? -1 : Math.min(wl.text.length, selEnd - wl.start);
  const hasSel = localStart >= 0 && localEnd >= 0 && localEnd > localStart;
  const cur = cursor === undefined ? -1 : cursor - wl.start;

  const segs: Segment[] = [];
  let i = 0;
  const flush = (to: number, sel: boolean, curMid?: number) => {
    const curAfter = curMid !== undefined && curMid >= i && curMid <= to;
    if (curAfter) {
      if (curMid! > i) segs.push({ text: wl.text.slice(i, curMid!), sel, cursor: false });
      segs.push({ text: "", sel, cursor: true });
      i = curMid!;
    }
    if (to > i) segs.push({ text: wl.text.slice(i, to), sel, cursor: false });
    i = to;
  };
  if (hasSel) {
    if (localStart > 0) flush(localStart, false, cur);
    flush(localEnd, true, cur);
    if (localEnd < wl.text.length) flush(wl.text.length, false, cur);
  } else {
    flush(wl.text.length, false, cur);
  }
  // 行尾光标
  if (cur >= 0 && cur <= wl.text.length && i === wl.text.length && !hasSel) {
    // 光标在行尾且未输出
    const placed = segs.some((s) => s.cursor);
    if (!placed && cur === wl.text.length) {
      segs.push({ text: "", sel: false, cursor: true });
    }
  }
  return segs;
}
