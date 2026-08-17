// 鼠标事件工具：ink 会把 SGR 鼠标序列剥去 ESC 后作为 input 传入
// （形如 `[<0;x;yM` 按下 / `[<0;x;ym` 释放），并广播给所有 useInput handler。
// 文本输入组件应忽略它们，避免鼠标序列被当作字符插入。

// isMouseSeq 判断 input 是否为终端控制序列（SGR 鼠标 `[<0;x;yM` 或其它 CSI `[?1006h` 等）。
// ink 把剥去 ESC 的 CSI 序列作为 input 广播给所有 useInput handler，
// 文本输入组件应忽略它们，避免被当作字符插入。
export function isMouseSeq(input: string): boolean {
  return /^\[[<?0-9;]/.test(input);
}

// parseMouseSeq 解析 SGR 鼠标事件，返回 {button, x, y, pressed}；非鼠标事件返回 null。
// pressed=true 表示按下（M），false 表示释放（m）。
export function parseMouseSeq(
  input: string,
): { button: number; x: number; y: number; pressed: boolean } | null {
  const m = input.match(/^\[<(\d+);(\d+);(\d+)([Mm])$/);
  if (!m) return null;
  return { button: Number(m[1]), x: Number(m[2]), y: Number(m[3]), pressed: m[4] === "M" };
}
