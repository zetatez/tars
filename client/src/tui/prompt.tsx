import React, { useEffect, useRef, useState } from "react";
import { Box, Text, useInput, useStdout } from "ink";
import { execFile } from "node:child_process";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { homedir } from "node:os";
import { theme, charWidth } from "./theme.js";
import { isMouseSeq } from "./mouse.js";
import { wrapLines, cursorPos, offsetAtCol, wordStarts, lineSegments, type WrappedLine } from "./text.js";
import { Autocomplete, isAutocompleteTriggered, type AutocompleteApi, type AutocompleteItem } from "./autocomplete.js";

const SPIN_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const HIST_MAX = 500;
const HIST_PATH = homedir() + "/.tars-cli-history";

function readClipboard(): Promise<string | null> {
  return new Promise((resolve) => {
    const cmds: Array<[string, string[]]> = [
      ["wl-paste", ["-n"]],
      ["xclip", ["-o", "-selection", "clipboard"]],
    ];
    const tryNext = (i: number) => {
      if (i >= cmds.length) return resolve(null);
      const [cmd, args] = cmds[i];
      execFile(cmd, args, { timeout: 1000 }, (err, stdout) => {
        if (!err && stdout) resolve(stdout.replace(/\r\n/g, "\n"));
        else tryNext(i + 1);
      });
    };
    tryNext(0);
  });
}

function writeClipboard(text: string): Promise<boolean> {
  return new Promise((resolve) => {
    const cmds: Array<[string, string[]]> = [
      ["wl-copy", []],
      ["xclip", ["-selection", "clipboard"]],
    ];
    const tryNext = (i: number) => {
      if (i >= cmds.length) return resolve(false);
      const [cmd, args] = cmds[i];
      const child = execFile(cmd, args, { timeout: 1000 }, (err) => {
        if (!err) resolve(true);
        else tryNext(i + 1);
      });
      child.stdin?.on("error", () => tryNext(i + 1));
      child.stdin?.end(text);
    };
    tryNext(0);
  });
}

export interface PromptProps {
  running: boolean;
  elapsed: number;
  escArmed: boolean;
  model: string;
  provider?: string;
  mode: "build" | "plan";
  serverUser: string;
  serverIp: string;
  everTyped: boolean;
  leaderActive: boolean;
  leaderHint?: string;
  inputLocked: boolean;
  maxWidth?: number;
  center?: boolean;
  tokens?: number;
  sessionInfo?: string;
  onSubmit: (text: string) => void;
  onExit: () => void;
  onInterrupt: () => void;
  onToggleMode: () => void;
  onTyped: () => void;
  onToast?: (text: string, kind?: "info" | "error" | "warning") => void;
  onSessionSwitch?: () => void;
  registerPrompt: (api: { setText(text: string): void } | null) => void;
}

export function Prompt({
  running,
  elapsed,
  escArmed,
  model,
  provider,
  mode,
  serverUser,
  serverIp,
  everTyped,
  leaderActive,
  leaderHint,
  inputLocked,
  maxWidth,
  center,
  tokens,
  sessionInfo,
  onSubmit,
  onExit,
  onInterrupt,
  onToggleMode,
  onTyped,
  onToast,
  onSessionSwitch,
  registerPrompt,
}: PromptProps) {
  const { stdout } = useStdout();
  const columns = stdout.columns ?? 80;
  const boxWidth = maxWidth ? Math.min(maxWidth, columns - 4) : columns - 4;
  const inputW = Math.max(8, boxWidth - 5);

  const [buf, setBuf] = useState("");
  const [cur, setCur] = useState(0);
  const [sel, setSel] = useState<number | null>(null);
  const [vimMode, setVimMode] = useState<"edit" | "norm">("edit");
  const pendingRef2 = useRef("");
  const [history, setHistory] = useState<string[]>([]);
  const [histIdx, setHistIdx] = useState(-1);
  const [spinnerFrame, setSpinnerFrame] = useState(0);
  const undoRef = useRef<Array<{ text: string; cur: number }>>([]);
  const redoRef = useRef<Array<{ text: string; cur: number }>>([]);
  const bufRef = useRef(buf);
  const curRef = useRef(cur);
  const selRef = useRef<typeof sel>(sel);
  const autoRef = useRef<AutocompleteApi | null>(null);
  bufRef.current = buf;
  curRef.current = cur;
  selRef.current = sel;

  useEffect(() => {
    if (!running) return;
    // spinner 帧率 150ms（约 6.7fps）：running 期间每帧都会全屏重绘，
    // 帧率过高在慢终端/SSH 下会残留旧帧，导致输入/消息文本"闪烁、显示多次"。
    const t = setInterval(() => setSpinnerFrame((f) => f + 1), 150);
    return () => clearInterval(t);
  }, [running]);

  // 加载历史命令（从 ~/.tars-cli-history）
  useEffect(() => {
    readFile(HIST_PATH, { encoding: "utf8" })
      .then((data) => {
        const lines = data.split("\n").filter((l) => l.length > 0);
        if (lines.length > 0) setHistory(lines);
      })
      .catch(() => {});
  }, []);

  const wrapped: WrappedLine[] = React.useMemo(() => wrapLines(buf, inputW), [buf, inputW]);
  const pos = cursorPos(wrapped, cur);
  const autoTrigger = isAutocompleteTriggered(buf, cur);

  function mutate(text: string, cursor: number, pushUndo = true) {
    if (pushUndo) {
      undoRef.current.push({ text: bufRef.current, cur: curRef.current });
      if (undoRef.current.length > 200) undoRef.current.shift();
      redoRef.current = [];
    }
    setBuf(text);
    setCur(Math.max(0, Math.min(text.length, cursor)));
    setSel(null);
  }

  function undo() {
    const item = undoRef.current.pop();
    if (!item) return;
    redoRef.current.push({ text: bufRef.current, cur: curRef.current });
    setBuf(item.text);
    setCur(item.cur);
  }
  function redo() {
    const item = redoRef.current.pop();
    if (!item) return;
    undoRef.current.push({ text: bufRef.current, cur: curRef.current });
    setBuf(item.text);
    setCur(item.cur);
  }

  function logicalLineBounds(text: string, cursor: number): { start: number; end: number } {
    const start = text.lastIndexOf("\n", cursor - 1) + 1;
    let end = text.indexOf("\n", cursor);
    if (end === -1) end = text.length;
    return { start, end };
  }

  function moveWord(dir: -1 | 1) {
    const { start, end } = logicalLineBounds(bufRef.current, curRef.current);
    const line = bufRef.current.slice(start, end);
    const local = curRef.current - start;
    const starts = wordStarts(line);
    if (dir === -1) {
      let t = -1;
      for (const s of starts) {
        if (s < local) t = s;
        else break;
      }
      setCur(start + (t === -1 ? 0 : t));
    } else {
      let t = line.length;
      for (const s of starts) {
        if (s > local) {
          t = s;
          break;
        }
      }
      setCur(start + t);
    }
    setSel(null);
  }

  function deleteWordBack() {
    const { start } = logicalLineBounds(bufRef.current, curRef.current);
    const local = curRef.current - start;
    const line = bufRef.current.slice(start, curRef.current);
    const starts = wordStarts(line);
    let t = 0;
    for (const s of starts) {
      if (s < local) t = s;
      else break;
    }
    const text = bufRef.current.slice(0, start + t) + bufRef.current.slice(curRef.current);
    mutate(text, start + t);
  }
  function deleteWordFwd() {
    const { end } = logicalLineBounds(bufRef.current, curRef.current);
    const line = bufRef.current.slice(curRef.current, end);
    const starts = wordStarts(line);
    let t = line.length;
    for (const s of starts) {
      if (s > 0) {
        t = s;
        break;
      }
    }
    const text = bufRef.current.slice(0, curRef.current) + bufRef.current.slice(curRef.current + t);
    mutate(text, curRef.current);
  }
  function deleteToLineEnd() {
    const { end } = logicalLineBounds(bufRef.current, curRef.current);
    const text = bufRef.current.slice(0, curRef.current) + bufRef.current.slice(end);
    mutate(text, curRef.current);
  }
  function deleteToLineStart() {
    const { start } = logicalLineBounds(bufRef.current, curRef.current);
    const text = bufRef.current.slice(0, start) + bufRef.current.slice(curRef.current);
    mutate(text, start);
  }
  function moveVisual(dir: -1 | 1) {
    const target = pos.line + dir;
    if (target < 0 || target >= wrapped.length) return;
    const off = offsetAtCol(wrapped[target], pos.col);
    setCur(off);
    setSel(null);
  }
  function visualHome() {
    setCur(wrapped[pos.line].start);
    setSel(null);
  }
  function visualEnd() {
    const wl = wrapped[pos.line];
    setCur(wl.start + wl.text.length);
    setSel(null);
  }

  function submit(text: string) {
    if (!text.trim()) return;
    onTyped();
    setHistory((h) => {
      const next = [...h, text].slice(-HIST_MAX);
      writeFile(HIST_PATH, next.join("\n") + "\n", { mode: 0o600 }).catch(() => {});
      return next;
    });
    setHistIdx(-1);
    mutate("", 0, false);
    setVimMode("edit");
    onSubmit(text);
  }

  function historyMove(dir: -1 | 1) {
    const h = history;
    if (h.length === 0) return;
    let idx = histIdx;
    if (dir === -1) {
      idx = idx < 0 ? h.length - 1 : Math.max(0, idx - 1);
      setHistIdx(idx);
      mutate(h[idx] ?? "", h[idx]?.length ?? 0, false);
    } else {
      idx = idx < 0 ? -1 : Math.min(h.length, idx + 1);
      if (idx === h.length) {
        setHistIdx(-1);
        mutate("", 0, false);
      } else {
        setHistIdx(idx);
        mutate(h[idx] ?? "", h[idx]?.length ?? 0, false);
      }
    }
  }

  function insert(text: string) {
    if (text.length > 0) onTyped();
    const clean = text.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
    mutate(bufRef.current.slice(0, curRef.current) + clean + bufRef.current.slice(curRef.current), curRef.current + clean.length);
  }

  function handleAutoSelect(item: AutocompleteItem) {
    if (item.kind === "command") {
      autoRef.current = null;
      // 补全只替换当前命令，保留编辑状态；Enter 再执行，避免选择后旧文本残留。
      mutate(item.insert, item.insert.length);
    } else {
      // @ 文件：把 [@..cur) 的 token 替换为选中路径
      const at = buf.lastIndexOf("@", cur - 1);
      if (at !== -1) {
        const text = buf.slice(0, at) + item.insert + " " + buf.slice(cur);
        mutate(text, at + item.insert.length + 1);
      }
      autoRef.current = null;
    }
  }

  useInput((input, key) => {
    if (isMouseSeq(input)) return; // 忽略鼠标事件，避免其被当作字符插入
    if (inputLocked || leaderActive) return;

    // 终端可能把 "文本+回车" 合并为单个 input（快速输入/粘贴）。拆分开：先插入文本，再按回车处理。
    if (input.includes("\r") || input.includes("\n")) {
      const idx = input.search(/[\r\n]/);
      const textPart = input.slice(0, idx);
      const full = textPart
        ? bufRef.current.slice(0, curRef.current) + textPart + bufRef.current.slice(curRef.current)
        : bufRef.current;
      if (textPart) insert(textPart);
      if (input[idx] === "\r" && !key.shift) {
        const candidate = full.trim();
        if (candidate) submit(candidate);
        return;
      }
      return;
    }

    // ---- vim norm 模式 ----
    if (vimMode === "norm") {
      const pk = pendingRef2.current;
      if (key.escape) {
        pendingRef2.current = "";
        // 第一次 Esc 预备打断（onInterrupt 内部处理 escArmed），第二次真正打断。
        // 空闲时 Esc 保持 norm 模式。
        onInterrupt();
        return;
      }
      if (key.return) {
        pendingRef2.current = "";
        const text = bufRef.current;
        if (text.trim()) submit(text);
        return;
      }
      if (key.upArrow || input === "k") {
        pendingRef2.current = "";
        historyMove(-1);
        return;
      }
      if (key.downArrow || input === "j") {
        pendingRef2.current = "";
        historyMove(1);
        return;
      }
      if (key.leftArrow || input === "h") {
        pendingRef2.current = "";
        setCur(Math.max(0, curRef.current - 1));
        setSel(null);
        return;
      }
      if (key.rightArrow || input === "l") {
        pendingRef2.current = "";
        setCur(Math.min(bufRef.current.length, curRef.current + 1));
        setSel(null);
        return;
      }
      if (pk === "d" && input === "d") {
        pendingRef2.current = "";
        mutate("", 0);
        return;
      }
      if (pk === "d") {
        pendingRef2.current = "";
        const { end } = logicalLineBounds(bufRef.current, curRef.current);
        const text = bufRef.current.slice(0, curRef.current) + bufRef.current.slice(end);
        mutate(text, curRef.current);
        return;
      }
      if (pk === "y" && input === "y") {
        pendingRef2.current = "";
        const { start, end } = logicalLineBounds(bufRef.current, curRef.current);
        void writeClipboard(bufRef.current.slice(start, end)).then((ok) =>
          onToast?.(ok ? "yanked line" : "no clipboard tool", ok ? "info" : "error"),
        );
        return;
      }
      if (input === "i" || input === "a" || input === "A") {
        pendingRef2.current = "";
        if (input === "a") setCur(Math.min(bufRef.current.length, curRef.current + 1));
        else if (input === "A") setCur(bufRef.current.length);
        setVimMode("edit");
        setSel(null);
        return;
      }
      if (input === "0") {
        pendingRef2.current = "";
        setCur(logicalLineBounds(bufRef.current, curRef.current).start);
        setSel(null);
        return;
      }
      if (input === "$") {
        pendingRef2.current = "";
        setCur(logicalLineBounds(bufRef.current, curRef.current).end);
        setSel(null);
        return;
      }
      if (input === "x") {
        pendingRef2.current = "";
        if (bufRef.current.length > 0) {
          const at = Math.min(curRef.current, bufRef.current.length - 1);
          mutate(bufRef.current.slice(0, at) + bufRef.current.slice(at + 1), at);
        }
        return;
      }
      if (input === "z") {
        pendingRef2.current = "";
        onSessionSwitch?.();
        return;
      }
      if (input === "u") {
        pendingRef2.current = "";
        undo();
        return;
      }
      if (key.ctrl && input === "r") {
        pendingRef2.current = "";
        redo();
        return;
      }
      if (input === "d" || input === "y") {
        pendingRef2.current = input;
        return;
      }
      // 其它键：作为 pending 处理（支持 dd 等双键），也允许 i/o 之外的导航
      pendingRef2.current = "";
      return;
    }

    if (autoTrigger && autoRef.current) {
      if (key.upArrow) {
        autoRef.current.move(-1);
        return;
      }
      if (key.downArrow) {
        autoRef.current.move(1);
        return;
      }
      if (key.return || key.tab) {
        autoRef.current.pick();
        return;
      }
      if (key.escape) {
        autoRef.current = null;
        return;
      }
      // 其它键继续走正常编辑（可继续输入 /cmd 内容）
    }

    if (key.ctrl && input === "c") {
      if (bufRef.current.trim()) mutate("", 0);
      else onExit();
      return;
    }
    if (key.ctrl && input === "v") {
      void readClipboard().then((t) => {
        if (t) insert(t);
      });
      return;
    }
    if (key.ctrl && input === "-") {
      undo();
      return;
    }
    if (key.ctrl && input === ".") {
      redo();
      return;
    }
    if (key.ctrl && input === "k") {
      deleteToLineEnd();
      return;
    }
    if (key.ctrl && input === "u") {
      deleteToLineStart();
      return;
    }
    if (key.ctrl && input === "w") {
      deleteWordBack();
      return;
    }
    if (key.ctrl && input === "d") {
      if (bufRef.current === "") onExit();
      else {
        mutate(bufRef.current.slice(0, curRef.current) + bufRef.current.slice(curRef.current + 1), curRef.current);
      }
      return;
    }
    if (key.ctrl && input === "a") {
      setCur(logicalLineBounds(bufRef.current, curRef.current).start);
      setSel(null);
      return;
    }
    if (key.ctrl && input === "e") {
      setCur(logicalLineBounds(bufRef.current, curRef.current).end);
      setSel(null);
      return;
    }
    if (key.ctrl && input === "b") {
      setCur(Math.max(0, curRef.current - 1));
      setSel(null);
      return;
    }
    if (key.ctrl && input === "f") {
      setCur(Math.min(bufRef.current.length, curRef.current + 1));
      setSel(null);
      return;
    }
    if (key.ctrl && input === "j") {
      insert("\n");
      return;
    }

    if (key.escape) {
      setVimMode("norm");
      setSel(null);
      return;
    }
    if (key.return && !key.shift && !key.meta && !key.ctrl) {
      const text = bufRef.current;
      if (text.trim()) submit(text);
      return;
    }
    if ((key.shift && key.return) || (key.meta && (key.return || input === "j" || input === "m"))) {
      insert("\n");
      return;
    }
    if (key.backspace) {
      if (curRef.current > 0) {
        mutate(bufRef.current.slice(0, curRef.current - 1) + bufRef.current.slice(curRef.current), curRef.current - 1);
      }
      return;
    }
    if (key.delete) {
      if (curRef.current < bufRef.current.length) {
        mutate(bufRef.current.slice(0, curRef.current) + bufRef.current.slice(curRef.current + 1), curRef.current);
      }
      return;
    }
    if (key.upArrow) {
      if (pos.line === 0 && pos.col === 0) historyMove(-1);
      else moveVisual(-1);
      return;
    }
    if (key.downArrow) {
      const lastW = wrapped[wrapped.length - 1];
      const atEnd = pos.line === wrapped.length - 1 && pos.col >= charWidth(lastW.text);
      if (atEnd) historyMove(1);
      else moveVisual(1);
      return;
    }
    if (key.leftArrow) {
      setCur(Math.max(0, curRef.current - 1));
      setSel(null);
      return;
    }
    if (key.rightArrow) {
      setCur(Math.min(bufRef.current.length, curRef.current + 1));
      setSel(null);
      return;
    }
    if (key.home) {
      setCur(logicalLineBounds(bufRef.current, curRef.current).start);
      setSel(null);
      return;
    }
    if (key.end) {
      setCur(logicalLineBounds(bufRef.current, curRef.current).end);
      setSel(null);
      return;
    }
    if (key.pageUp || key.pageDown) return;
    if (key.tab) {
      // Tab：自动补全可见时已完成（见上方）；否则切换 build/plan 模式
      onToggleMode();
      return;
    }
    if (key.meta) {
      const c = input.toLowerCase();
      if (c === "b") moveWord(-1);
      else if (c === "f") moveWord(1);
      else if (c === "d") deleteWordFwd();
      else if (c === "a") visualHome();
      else if (c === "e") visualEnd();
      return;
    }
    if (key.shift && key.leftArrow) {
      const base = selRef.current ?? curRef.current;
      setCur(Math.max(0, curRef.current - 1));
      setSel(base);
      return;
    }
    if (key.shift && key.rightArrow) {
      const base = selRef.current ?? curRef.current;
      setCur(Math.min(bufRef.current.length, curRef.current + 1));
      setSel(base);
      return;
    }
    if (input && !key.ctrl && !key.meta && !key.shift) {
      insert(input);
    }
  });

  useEffect(() => {
    registerPrompt({
      setText(text: string) {
        mutate(text, text.length, false);
      },
    });
    return () => registerPrompt(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [registerPrompt]);

  const borderColor = leaderActive || vimMode === "norm" ? theme.borderActive : theme.borderMuted;
  const selRange = sel === null ? undefined : { start: Math.min(sel, cur), end: Math.max(sel, cur) };

  const promptBox = (
    <Box width={boxWidth} flexDirection="column" flexShrink={0}>
      <Box flexDirection="row" justifyContent="space-between" marginTop={1} paddingLeft={1}>
        <Box flexDirection="row" gap={1} flexShrink={1} overflowX="hidden">
          <Text color={mode === "plan" ? theme.success : theme.secondary} bold>
            {mode === "plan" ? "Plan" : "Build"}
          </Text>
          <Text color={theme.dim}>·</Text>
          <Text color={theme.dim} wrap="truncate">
            {serverUser || "-"}@{serverIp}
          </Text>
        </Box>
        {tokens != null || model ? (
          <Box flexDirection="row" gap={1} flexShrink={0} overflowX="hidden">
            {tokens != null ? (
              <>
                <Text color={theme.dim}>{`~${tokens} tokens`}</Text>
                <Text color={theme.dim}>·</Text>
              </>
            ) : null}
            {model ? (
              <Text color={theme.dim}>
                {provider ? `${provider}:` : ""}
                {model}
              </Text>
            ) : null}
          </Box>
        ) : null}
      </Box>
      <Box flexDirection="row" alignItems="center" gap={1}>
        <Box flexGrow={1} flexDirection="column" borderStyle="single" borderColor={borderColor} backgroundColor={theme.inputBg} borderLeft={false} borderRight={false}>
        <Box flexDirection="column" paddingX={1} paddingY={0}>
          {buf === "" && !everTyped ? (
            <Box flexDirection="row">
              <Text inverse>A</Text>
              <Text color={theme.inputPlaceholder}>sk anything...</Text>
            </Box>
          ) : (
            wrapped.map((wl, i) => {
              const segs = lineSegments(wl, selRange?.start, selRange?.end, i === pos.line ? cur : undefined);
              return (
                <Box key={i} flexDirection="row">
                  {segs.map((s, j) =>
                    s.cursor ? (
                      <Text key={j} inverse>
                        {" "}
                      </Text>
                    ) : s.sel ? (
                      <Text key={j} inverse>
                        {s.text}
                      </Text>
                    ) : (
                      <Text key={j} color={theme.inputText}>
                        {s.text}
                      </Text>
                    ),
                  )}
                </Box>
              );
            })
          )}
        </Box>
        </Box>
        <Box flexShrink={0} alignItems="center" backgroundColor={vimMode === "norm" ? theme.warning : theme.darkGray} paddingX={1}>
          <Text color={vimMode === "norm" ? "#000000" : theme.text} bold>
            {vimMode === "norm" ? "N" : "E"}
          </Text>
        </Box>
      </Box>
    </Box>
  );

  return (
    <Box flexDirection="column" flexShrink={0} alignItems={center ? "center" : undefined} width={center ? "100%" : undefined}>
      {leaderActive && leaderHint ? (
        <Box width={boxWidth} marginBottom={1} backgroundColor={theme.primary} paddingX={1} flexShrink={0}>
          <Text color={theme.background} bold wrap="truncate">
            {leaderHint}
          </Text>
        </Box>
      ) : null}
      {promptBox}
      {autoTrigger ? (
        <Box width={boxWidth} marginTop={1}>
          <Autocomplete value={buf} cursor={cur} width={inputW} onSelect={handleAutoSelect} register={(api) => (autoRef.current = api)} />
        </Box>
      ) : null}
      {sessionInfo ? (
        <Box width={boxWidth} flexDirection="row" justifyContent="space-between" paddingX={1} paddingTop={0}>
          <Box flexDirection="row" gap={1} flexShrink={1} overflowX="hidden">
            {running ? (
              <Text color={theme.textMuted}>{SPIN_FRAMES[spinnerFrame % SPIN_FRAMES.length]}</Text>
            ) : (
              <Text color={theme.textMuted}>⠂</Text>
            )}
            <Text color={theme.textMuted} wrap="truncate">
              {running ? `Running… ${elapsed}s` : "idle"}
            </Text>
          </Box>
          <Box flexShrink={0}>
            <Text color={theme.textMuted} wrap="truncate">
              {sessionInfo}
            </Text>
          </Box>
        </Box>
      ) : null}
    </Box>
  );
}
