import React, { useEffect, useRef, useState } from "react";
import { Box, Text, useInput, useStdout } from "ink";
import { execFile } from "node:child_process";
import { theme, charWidth } from "./theme.js";
import { wrapLines, cursorPos, offsetAtCol, wordStarts, lineSegments, type WrappedLine } from "./text.js";
import { Autocomplete, isAutocompleteTriggered, type AutocompleteApi, type AutocompleteItem } from "./autocomplete.js";

const SPIN_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];

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

export interface PromptProps {
  running: boolean;
  elapsed: number;
  escArmed: boolean;
  model: string;
  provider?: string;
  mode: "build" | "plan";
  serverUser: string;
  serverIp: string;
  clientUser: string;
  clientIp: string;
  everTyped: boolean;
  leaderActive: boolean;
  inputLocked: boolean;
  maxWidth?: number;
  center?: boolean;
  tokens?: number;
  onSubmit: (text: string) => void;
  onExit: () => void;
  onInterrupt: () => void;
  onToggleMode: () => void;
  onTyped: () => void;
  onCommandSelect: (cmdText: string) => void;
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
  clientUser,
  clientIp,
  everTyped,
  leaderActive,
  inputLocked,
  maxWidth,
  center,
  tokens,
  onSubmit,
  onExit,
  onInterrupt,
  onToggleMode,
  onTyped,
  onCommandSelect,
  registerPrompt,
}: PromptProps) {
  const { stdout } = useStdout();
  const columns = stdout.columns ?? 80;
  const boxWidth = maxWidth ? Math.min(maxWidth, columns - 4) : columns - 4;
  const inputW = Math.max(8, boxWidth - 5);

  const [buf, setBuf] = useState("");
  const [cur, setCur] = useState(0);
  const [sel, setSel] = useState<number | null>(null);
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
    const t = setInterval(() => setSpinnerFrame((f) => f + 1), 60);
    return () => clearInterval(t);
  }, [running]);

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
    setHistory((h) => [...h, text]);
    setHistIdx(-1);
    mutate("", 0, false);
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
      onCommandSelect(item.insert.trim());
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
    if (inputLocked || leaderActive) return;
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
      if (running) onInterrupt();
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

  const borderColor = leaderActive ? theme.borderActive : theme.borderMuted;
  const selRange = sel === null ? undefined : { start: Math.min(sel, cur), end: Math.max(sel, cur) };

  const promptBox = (
    <Box width={boxWidth} flexDirection="column" flexShrink={0}>
      <Box flexDirection="column" borderStyle="single" borderColor={borderColor} backgroundColor={theme.inputBg}>
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
      {running ? (
        <Box flexDirection="row" gap={1} marginTop={1} paddingX={1}>
          <Text color={theme.accent}>{SPIN_FRAMES[spinnerFrame % SPIN_FRAMES.length]}</Text>
          <Text color={theme.text}>Running… {elapsed}s</Text>
          <Text>
            esc{" "}
            <Text color={escArmed ? theme.accent : theme.textMuted}>
              {escArmed ? "again to interrupt" : "interrupt"}
            </Text>
          </Text>
        </Box>
      ) : null}
      <Box flexDirection="row" justifyContent="space-between" marginTop={1} paddingX={1}>
        <Box flexDirection="row" gap={1} flexShrink={1} overflowX="hidden">
          <Text color={theme.accent} wrap="truncate">
            {serverUser || "-"}@{serverIp}
          </Text>
          <Text color={theme.textMuted}>→</Text>
          <Text color={theme.textMuted} wrap="truncate">
            {clientUser || "-"}@{clientIp || "?"}
          </Text>
        </Box>
        <Box flexDirection="row" gap={1} flexShrink={0}>
          <Text color={theme.textMuted}>·</Text>
          <Text color={mode === "plan" ? theme.accent : theme.secondary} bold>
            {mode === "plan" ? "Plan" : "Build"}
          </Text>
          <Text color={theme.textMuted}>·</Text>
          <Text color={theme.textMuted}>{tokens != null ? `~${tokens} tokens` : "-"}</Text>
          <Text color={theme.textMuted}>·</Text>
          <Text color={theme.text}>
            {provider ? `${provider}:` : ""}
            {model || "tars"}
          </Text>
        </Box>
      </Box>
    </Box>
  );

  return (
    <Box flexDirection="column" flexShrink={0} alignItems={center ? "center" : undefined} width={center ? "100%" : undefined}>
      {autoTrigger ? (
        <Box width={boxWidth} marginBottom={1}>
          <Autocomplete value={buf} cursor={cur} width={inputW} onSelect={handleAutoSelect} register={(api) => (autoRef.current = api)} />
        </Box>
      ) : null}
      {promptBox}
    </Box>
  );
}
