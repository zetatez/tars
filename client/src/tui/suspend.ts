type SuspendImpl = (callback: () => void | Promise<void>) => Promise<void>;

// Ink 的 suspendTerminal：把终端完整交给子进程（清掉当前帧/关 raw mode/bracketed paste、
// 暂停 Ink 输入监听），结束后强制整帧重绘。由 TuiApp 通过 setSuspendImpl 注册。
let suspendImpl: SuspendImpl | null = null;

export function setSuspendImpl(impl: SuspendImpl | null): void {
  suspendImpl = impl;
}

// 在终端被完整交给子进程的情况下运行 fn；否则退化为手动关闭 raw mode。
// 供外部编辑器、交互式 ssh 等"临时让出终端"的场景使用。
export async function withSuspended<T>(fn: () => Promise<T>): Promise<T> {
  if (suspendImpl) {
    let result!: T;
    await suspendImpl(async () => {
      // 关键：Node 的 tty stdin 处于 flowing 模式时会对 fd 0 持续发起 read()，
      // 会抢走子进程（vim/ssh 等 raw 模式程序）本应读到的按键。这里暂停 Node 的
      // 读取并等待在途 read() 结束，让子进程独占终端输入；结束后恢复。
      process.stdin.pause();
      await new Promise((r) => setImmediate(r));
      try {
        result = await fn();
      } finally {
        process.stdin.resume();
      }
    });
    return result;
  }
  const wasRaw = process.stdin.isRaw;
  process.stdin.setRawMode(false);
  process.stdin.pause();
  try {
    return await fn();
  } finally {
    process.stdin.resume();
    if (wasRaw) {
      try {
        process.stdin.setRawMode(true);
      } catch {
        // 忽略
      }
    }
  }
}