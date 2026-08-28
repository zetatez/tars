import React, { useCallback, useEffect, useRef, useState } from "react";
import { Box, Text, useApp, useInput, useStdout, measureElement, type DOMElement } from "ink";
import { execFile } from "node:child_process";
import { writeFile } from "node:fs/promises";
import { lookup } from "node:dns/promises";
import path from "node:path";

import { API } from "../api.js";
import { streamEvents } from "../sse.js";
import type { EventData, Message } from "../types.js";
import { theme, charWidth } from "./theme.js";
import { MessageView, measureMessages, type ToolRegion } from "./messages.js";
import { isMouseSeq, parseMouseSeq } from "./mouse.js";
import { Prompt } from "./prompt.js";
import { DialogSelect, DialogConfirm, DialogHelp, DialogApproval, LeaderMenu, SessionSwitcher, type SelectOption, type LeaderItem } from "./dialog.js";
import { SLASH_COMMANDS } from "./keys.js";
import { openEditor } from "./editor.js";
import { setSuspendImpl } from "./suspend.js";
import { runSsh, runVim, runBang } from "./commands.js";
import { type SshTarget } from "./ssh.js";

// 行级平滑滚动的步长（单位：行）。滚轮每格 3 行；PageUp/Down 也走平滑步长而非整页跳转。
const WHEEL_STEP = 3;

// 测量包装：为消息挂载 ref，布局后把实际渲染行数（不含 marginTop）回传给父组件。
// measureElement 在溢出裁剪盒内仍返回自然内容行数，可安全用于行级滚动窗口计算。
function MeasuredBox({
  seq,
  onMeasured,
  children,
}: {
  seq: number;
  onMeasured: (seq: number, contentRows: number) => void;
  children: React.ReactNode;
}) {
  const ref = useRef<DOMElement | null>(null);
  useEffect(() => {
    if (!ref.current) return;
    onMeasured(seq, measureElement(ref.current).height);
  });
  return (
    <Box ref={ref} flexDirection="column" flexShrink={0} width="100%">
      {children}
    </Box>
  );
}

async function resolveIp(host: string): Promise<string> {
  if (!host) return host;
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.includes(":")) return host;
  if (host === "localhost") return "127.0.0.1";
  try {
    const { address } = await lookup(host, { family: 4 });
    return address;
  } catch {
    return host;
  }
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
      const child = execFile(cmd, args, { timeout: 2000 }, (err) => {
        if (err) tryNext(i + 1);
        else resolve(true);
      });
      child.stdin?.write(text);
      child.stdin?.end();
    };
    tryNext(0);
  });
}

function transcriptLines(messages: Message[]): string {
  const lines: string[] = [];
  for (const m of messages) {
    if (m.role === "user") lines.push(`## User\n\n${m.content.text ?? ""}\n`);
    else {
      for (const t of m.content.tools ?? []) {
        lines.push(`### tool: ${t.name}\n\`\`\`json\n${JSON.stringify(t, null, 2)}\n\`\`\`\n`);
      }
      if (m.content.text) lines.push(`## Assistant\n\n${m.content.text}\n`);
    }
  }
  return lines.join("\n");
}

type View = { type: "home" } | { type: "session"; id: string };

// 粗略估算会话已用 token（与 pi/opencode 一致的 char/4 启发式），仅用于底部展示。
function estimateTokens(messages: Message[]): number {
  let chars = 0;
  for (const m of messages) {
    const c = m.content;
    if (c?.text) chars += c.text.length;
    if (c?.tools) for (const t of c.tools) chars += JSON.stringify(t.args ?? "").length + (JSON.stringify(t.result ?? "")).length;
    if (c?.error) chars += c.error.length;
  }
  return Math.round(chars / 4);
}

// 估算一段 markdown 文本渲染后占用的行数（近似，用于消息区滚动窗口裁剪）。
function estimateTextLines(text: string, contentWidth: number): number {
  if (!text) return 0;
  let lines = 0;
  for (const raw of text.replace(/\r\n/g, "\n").split("\n")) {
    if (/^\s*$/.test(raw)) continue;
    const seg = raw.trim();
    // 代码块：行数 + 边框上下各 1
    if (/^```/.test(raw)) {
      lines += 1; // 边框或语言行
      continue;
    }
    if (/^\s*\|/.test(raw) && /^\s*\|[\s:|-]+\|\s*$/.test(raw)) continue; // 表格分隔行
    const w = charWidth(seg);
    lines += Math.max(1, Math.ceil(w / Math.max(1, contentWidth)));
  }
  return lines + 2; // 顶部边框 + 底部边框（近似代码块/块间距）
}

// 估算一条消息渲染后占用的行数（近似）。
function estimateMessageLines(m: Message, contentWidth: number): number {
  const c = m.content;
  let lines = 1; // marginTop
  if (m.role === "user") {
    lines += 2; // paddingY
    lines += estimateTextLines(c.text ?? "", contentWidth);
    lines += (c.files?.length ?? 0);
    return lines;
  }
  if (c.error) {
    lines += 2 + 1; // paddingY + 错误文本行
    return lines;
  }
  for (const _t of c.tools ?? []) {
    lines += 2; // paddingY
    lines += 2; // 标题行 + 状态行
    lines += 1; // marginTop
    // 折叠显示的前几行 + 折叠提示行（约 COLLAPSE_RENDER_LINES=12，取中）
    lines += 8;
    lines += 1; // 展开态额外的提示行可忽略
  }
  if (c.text) lines += estimateTextLines(c.text, contentWidth);
  return lines;
}

export function TuiApp({
  api,
  sessionId,
  initialMessages,
  initialPrompt,
  sshTarget,
}: {
  api: API;
  sessionId?: string;
  initialMessages?: Message[];
  initialPrompt?: string;
  sshTarget: SshTarget;
}) {
  const { exit, suspendTerminal } = useApp();
  const [view, setView] = useState<View>(sessionId ? { type: "session", id: sessionId } : { type: "home" });
  const [pendingPrompt, setPendingPrompt] = useState<string | undefined>(initialPrompt);
  const [mode, setMode] = useState<"build" | "plan">("build");
  const [everTyped, setEverTyped] = useState(false);

  useEffect(() => {
    setSuspendImpl(suspendTerminal);
    return () => setSuspendImpl(null);
  }, [suspendTerminal]);

  const toggleMode = () => setMode((m) => (m === "build" ? "plan" : "build"));
  const setModeDirect = (m: "build" | "plan") => setMode(m);
  const markTyped = () => setEverTyped(true);

  // agent server host：从 base-url 推断（footer 展示）
  const host = (() => {
    try {
      return new URL(api.baseURL).hostname;
    } catch {
      return api.baseURL;
    }
  })();
  const serverUser = sshTarget.user ?? "";

  const [serverIp, setServerIp] = useState(host);
  useEffect(() => {
    let alive = true;
    void resolveIp(host).then((ip) => alive && setServerIp(ip));
    return () => { alive = false; };
  }, [host]);

  const quit = () => {
    exit();
    setTimeout(() => process.exit(0), 100);
  };

  return (
    <Box flexDirection="column" height="100%">
      <Box flexGrow={1} flexDirection="column" minHeight={0}>
        {view.type === "home" ? (
          <HomeView
            api={api}
            sshTarget={sshTarget}
            mode={mode}
            serverUser={serverUser}
            serverIp={serverIp}
            onToggleMode={toggleMode}
            onSetMode={setModeDirect}
            everTyped={everTyped}
            onTyped={markTyped}
            onStart={async (text) => {
              const s = await api.createSession();
              setPendingPrompt(text);
              setView({ type: "session", id: s.id });
            }}
            onResume={(id) => {
              setPendingPrompt(undefined);
              setView({ type: "session", id });
            }}
            onExit={quit}
          />
        ) : (
          <SessionView
            key={view.id}
            api={api}
            sshTarget={sshTarget}
            sessionId={view.id}
            initialMessages={initialMessages && view.id === sessionId ? initialMessages : []}
            initialPrompt={pendingPrompt}
            mode={mode}
            serverUser={serverUser}
            serverIp={serverIp}
            onToggleMode={toggleMode}
            onSetMode={setModeDirect}
            everTyped={everTyped}
            onTyped={markTyped}
            onNew={() => {
              setPendingPrompt(undefined);
              setView({ type: "home" });
            }}
            onSessionChange={(id) => setView({ type: "session", id })}
            onExit={quit}
          />
        )}
      </Box>
    </Box>
  );
}

function HomeView({
  api,
  sshTarget,
  mode,
  serverUser,
  serverIp,
  onToggleMode,
  onSetMode,
  everTyped,
  onTyped,
  onStart,
  onResume,
  onExit,
}: {
  api: API;
  sshTarget: SshTarget;
  mode: "build" | "plan";
  serverUser: string;
  serverIp: string;
  onToggleMode: () => void;
  onSetMode: (m: "build" | "plan") => void;
  everTyped: boolean;
  onTyped: () => void;
  onStart: (text: string) => Promise<void>;
  onResume: (id: string) => void;
  onExit: () => void;
}) {
  const [leaderPending, setLeaderPending] = useState(false);
  const [dialog, setDialog] = useState<null | { kind: "help" } | { kind: "models" } | { kind: "agents" } | { kind: "switch" } | { kind: "tools" }>(null);
  const [tools, setTools] = useState<SelectOption[]>([]);
  const [modelOptions, setModelOptions] = useState<SelectOption[]>([]);
  const [toast, setToast] = useState<{ text: string; kind: "info" | "error" | "warning" } | null>(null);
  const toastTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const promptApi = useRef<{ setText(text: string): void } | null>(null);

  const showToast = useCallback((text: string, kind: "info" | "error" | "warning" = "info") => {
    setToast({ text, kind });
    if (toastTimer.current) clearTimeout(toastTimer.current);
    toastTimer.current = setTimeout(() => setToast(null), 4000);
  }, []);

  useEffect(() => {
    if (!leaderPending) return;
    const t = setTimeout(() => setLeaderPending(false), 2000);
    return () => clearTimeout(t);
  }, [leaderPending]);

  const handleSubmit = (text: string) => {
    const trimmed = text.trim();
    if (trimmed === "exit" || trimmed === "quit" || trimmed === ":q") {
      onExit();
      return;
    }
    if (trimmed.startsWith("!") && trimmed.length > 1) {
      runBang(sshTarget, trimmed.slice(1).trim(), showToast);
      return;
    }
    if (trimmed.startsWith("/")) {
      const parts = trimmed.slice(1).split(/\s+/);
      const cmd = parts[0].toLowerCase();
      const arg = parts.slice(1).join(" ");
      const found = SLASH_COMMANDS.find((c) => c.name === cmd || c.aliases.includes(cmd));
      switch (found?.name) {
        case "new":
          showToast("已在首页，直接输入问题即可创建新会话", "info");
          break;
        case "sessions":
          openSessions();
          break;
        case "ssh":
          runSsh(sshTarget, showToast);
          break;
        case "vim":
          void runVim(sshTarget, arg, showToast);
          break;
        case "agents":
          setDialog({ kind: "agents" });
          break;
        case "models":
          void (async () => {
              try {
                const { models } = await api.models();
                if (!models.length) {
                  showToast("服务端未配置模型", "warning");
                  return;
                }
                setModelOptions(
                models.map((m) => ({ key: m.model, title: m.model, detail: m.provider || "默认" })),
              );
              setDialog({ kind: "models" });
            } catch (err) {
              showToast((err as Error).message, "error");
            }
          })();
          break;
        case "status":
          void (async () => {
            let version = "";
            try {
              const v = await api.version();
              version = v.version || "";
            } catch {}
            showToast([version ? `server: v${version}` : "server: -", `mode: ${mode}`].join("\n"), "info");
          })();
          break;
        case "init":
        case "themes":
        case "variants":
          showToast(`暂不支持 /${cmd}`, "warning");
          break;
        case "skills":
        case "mcps":
          void (async () => {
            try {
              const tools = await api.mcpTools();
              setTools(
                tools.map((t) => ({
                  key: t.name,
                  title: t.name,
                  detail: (t.description ?? "").slice(0, 80),
                })),
              );
              setDialog({ kind: "tools" });
            } catch (err) {
              showToast(`获取工具列表失败：${(err as Error).message}`, "error");
            }
          })();
          break;
        case "help":
          setDialog({ kind: "help" });
          break;
        case "editor":
          void openEditor("").then((content) => {
            if (content) promptApi.current?.setText(content);
          });
          break;
        case "exit":
          onExit();
          break;
        default:
          // 需要会话的命令（copy/export/rollback/delete）
          if (["copy", "export", "rollback", "delete"].includes(found?.name ?? "")) {
            showToast("请先创建会话（直接输入问题即可）", "warning");
          } else {
            showToast(`unknown command: /${cmd}`, "error");
          }
      }
      return;
    }
    void onStart(text).catch((err) => {
      showToast(`创建会话失败：${(err as Error).message}`, "error");
    });
  };

  const openSessions = () => setDialog({ kind: "switch" });

  useInput((input, key) => {
    if (isMouseSeq(input)) return;
    if (dialog) return;
    if (leaderPending) {
      const c = input.toLowerCase();
      if (c === "q") onExit();
      else if (c === "l" || c === "r") openSessions();
      else if (c === "h") setDialog({ kind: "help" });
      setLeaderPending(false);
      return;
    }
    if (key.ctrl && input === "x") setLeaderPending(true);
  });

  return (
    <Box flexDirection="column" height="100%">
      <Box flexGrow={1} flexDirection="column" justifyContent="center" alignItems="center" paddingLeft={2} paddingRight={2}>
        <Text bold color={theme.accent}>
          TARS
        </Text>
      </Box>
      <Box paddingLeft={2} paddingRight={2} paddingBottom={1} flexShrink={0}>
        <Prompt
          running={false}
          elapsed={0}
          escArmed={false}
          model=""
          mode={mode}
          serverUser={serverUser}
          serverIp={serverIp}
          everTyped={everTyped}
          leaderActive={leaderPending}
          leaderHint="l/r=sessions · h=help · q=exit"
          inputLocked={dialog !== null}
          onSubmit={handleSubmit}
          onExit={onExit}
          onInterrupt={() => {}}
          onToggleMode={onToggleMode}
          onTyped={onTyped}
          onToast={showToast}
          onSessionSwitch={() => setDialog({ kind: "switch" })}
          registerPrompt={(api2) => {
            promptApi.current = api2;
          }}
        />
      </Box>

      {dialog?.kind === "switch" ? (
        <SessionSwitcher
          api={api}
          onSelect={(id) => {
            setDialog(null);
            onResume(id);
          }}
          onCancel={() => setDialog(null)}
        />
      ) : null}

      {dialog?.kind === "tools" ? (
        <DialogSelect
          title="Tools (MCP)"
          options={tools}
          footerHint="↑↓ navigate · enter close · esc close"
          onSelect={() => setDialog(null)}
          onCancel={() => setDialog(null)}
        />
      ) : null}

      {dialog?.kind === "models" ? (
        <DialogSelect
          title="Models"
          options={modelOptions}
          onSelect={(o) => {
            setDialog(null);
            showToast(`模型选择仅对会话生效：先创建会话后再 /models 切换`, "info");
          }}
          onCancel={() => setDialog(null)}
        />
      ) : null}
      {dialog?.kind === "agents" ? (
        <DialogSelect
          title="Agents"
          options={[
            { key: "build", title: "Build", detail: "执行工具" },
            { key: "plan", title: "Plan", detail: "只读规划" },
          ]}
          onSelect={(o) => {
            setDialog(null);
            onSetMode(o.key as "build" | "plan");
            showToast(`agent → ${o.key}`);
          }}
          onCancel={() => setDialog(null)}
        />
      ) : null}
      {dialog?.kind === "help" ? <DialogHelp onClose={() => setDialog(null)} /> : null}

      {toast ? (
        <Box position="absolute" top={1} left={0} right={0} justifyContent="center" alignItems="center">
          <Box flexDirection="column" borderStyle="single" borderColor={theme.borderMuted} backgroundColor={theme.backgroundPanel}>
            <Box paddingLeft={1} paddingRight={1} paddingTop={1} paddingBottom={1}>
              <Text
                color={toast.kind === "error" ? theme.error : toast.kind === "warning" ? theme.warning : theme.text}
                wrap="wrap"
              >
                {toast.text}
              </Text>
            </Box>
          </Box>
        </Box>
      ) : null}
    </Box>
  );
}

type DialogState =
  | null
  | { kind: "switch" }
  | { kind: "tools" }
  | { kind: "models" }
  | { kind: "agents" }
  | { kind: "confirmDelete" }
  | { kind: "help" };

function SessionView({
  api,
  sshTarget,
  sessionId,
  initialMessages,
  initialPrompt,
  mode,
  serverUser,
  serverIp,
  onToggleMode,
  onSetMode,
  everTyped,
  onTyped,
  onNew,
  onExit,
  onSessionChange,
}: {
  api: API;
  sshTarget: SshTarget;
  sessionId: string;
  initialMessages: Message[];
  initialPrompt?: string;
  mode: "build" | "plan";
  serverUser: string;
  serverIp: string;
  onToggleMode: () => void;
  onSetMode: (m: "build" | "plan") => void;
  everTyped: boolean;
  onTyped: () => void;
  onNew: () => void;
  onExit: () => void;
  onSessionChange: (id: string) => void;
}) {
  const { stdout } = useStdout();
  const columns = stdout.columns ?? 80;
  const rows = stdout.rows ?? 24;
  // 消息区可视高度：终端行数扣除 session header 与底部输入区。
  const viewH = Math.max(5, rows - 8);

  const [sid, setSid] = useState(sessionId);
  const [messages, setMessages] = useState<Message[]>(initialMessages);
  const [status, setStatus] = useState<"idle" | "running">("idle");
  const [elapsed, setElapsed] = useState(0);
  const [cwd, setCwd] = useState("");
  const [model, setModel] = useState("");
  const [provider, setProvider] = useState("");
  const [toast, setToast] = useState<{ text: string; kind: "info" | "error" | "warning" } | null>(null);
  const [escArmed, setEscArmed] = useState(false);
  const [leaderPending, setLeaderPending] = useState(false);
  const [leaderFocus, setLeaderFocus] = useState(0);
  const [expandedTools, setExpandedTools] = useState<Set<string>>(new Set());
  const [toolRegions, setToolRegions] = useState<Map<string, ToolRegion>>(new Map());
  const [dialog, setDialog] = useState<DialogState>(null);
  const [tools, setTools] = useState<SelectOption[]>([]);
  const [modelOptions, setModelOptions] = useState<SelectOption[]>([]);
  const [approvals, setApprovals] = useState<Array<{ id: string; action: string; resource: string }>>([]);
  const loadGenerationRef = useRef(0);
  const approvalBusyRef = useRef(new Set<string>());

  const modelName = () => (provider ? `${provider}:` : "") + (model || "tars");
  // 高度缓存：seq → { h: 内容行数（不含 margin）, sig: 内容签名 }。
  // 精确高度来自离屏测量（measureMessages）；屏幕测量仅用于内容/折叠变化时（用签名判定），
  // 避免裁剪盒内被约束的错误测量污染精确值。
  const heightsRef = useRef(new Map<number, { h: number; sig: string }>());
  const [, setHver] = useState(0);
  const messagesRef = useRef(messages);
  messagesRef.current = messages;
  const expandedRef = useRef(expandedTools);
  expandedRef.current = expandedTools;
  const sigOf = (m: Message, exp: ReadonlySet<string>): string => {
    const c = m.content;
    const tools = (c.tools ?? [])
      .map((t) => `${t.id}:${exp.has(t.id) ? "E" : "C"}:${t.name}:${JSON.stringify(t.result ?? {}).length}`)
      .join(",");
    return `${c.text ?? ""}|${c.error ?? ""}|${tools}`;
  };
  const noteHeight = useCallback((seq: number, contentRows: number) => {
    const m = messagesRef.current.find((x) => x.seq === seq);
    if (!m) return;
    const sig = sigOf(m, expandedRef.current);
    const cur = heightsRef.current.get(seq);
    // 内容与折叠状态未变化：忽略屏幕测量（裁剪盒可能返回被约束的值），保留离屏精确高度。
    if (cur && cur.sig === sig) return;
    heightsRef.current.set(seq, { h: contentRows, sig });
    setHver((v) => v + 1);
  }, []);
  const lastSeqRef = useRef(initialMessages.length ? initialMessages[initialMessages.length - 1].seq : 0);
  const oldestSeqRef = useRef(initialMessages.length ? initialMessages[0].seq : 0);
  const loadingMoreRef = useRef(false);
  const pendingRef = useRef(false);
  const stopRef = useRef<(() => void) | null>(null);
  const toastTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const promptApiRef = useRef<{ setText(text: string): void } | null>(null);
  const sentInitial = useRef(!initialPrompt);
  const modeRef = useRef(mode);
  modeRef.current = mode;

  const contentWidth = Math.max(20, columns - 8);
  // 终端宽度变化 → 行数失效，清空高度缓存（离屏测量 effect 会重新测量）
  const cwRef = useRef(contentWidth);
  if (cwRef.current !== contentWidth) {
    cwRef.current = contentWidth;
    heightsRef.current.clear();
  }
  // 每条消息在列中占用的总行数 = margin(1) + 内容行数；内容行数单独取用。
  const totalOf = (m: Message): number => {
    const measured = heightsRef.current.get(m.seq);
    return measured !== undefined ? measured.h + 1 : Math.max(1, estimateMessageLines(m, contentWidth));
  };

  // 离屏精确测量：覆盖「从未测量」以及「工具折叠状态变化」的消息。
  // 新追加的流式消息由屏幕测量（noteHeight）在可见时补齐，不在此重复离屏测量。
  const toolsExpKeyRef = useRef("");
  useEffect(() => {
    const expKey = JSON.stringify([...expandedTools].sort());
    const expChanged = toolsExpKeyRef.current !== expKey;
    toolsExpKeyRef.current = expKey;
    const need = messages.filter((m) => {
      const cur = heightsRef.current.get(m.seq);
      if (!cur) return true;
      if (expChanged && (m.content.tools?.length ?? 0) > 0) return true;
      return false;
    });
    if (need.length === 0) return;
    const sigs = new Map<number, string>();
    for (const m of need) sigs.set(m.seq, sigOf(m, expandedTools));
    let cancelled = false;
    measureMessages(need, columns, expandedTools)
      .then((h) => {
        if (cancelled) return;
        let changed = false;
        for (const [seq, hh] of h) {
          const sig = sigs.get(seq) ?? "";
          const cur = heightsRef.current.get(seq);
          if (!cur || cur.h !== hh || cur.sig !== sig) {
            heightsRef.current.set(seq, { h: hh, sig });
            changed = true;
          }
        }
        if (changed) setHver((v) => v + 1);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [messages, expandedTools, columns]);
  const rowStart: number[] = [];  let acc = 0;
  for (const m of messages) {
    rowStart.push(acc);
    acc += totalOf(m);
  }
  const totalH = acc;
  const maxTop = Math.max(0, totalH - viewH);
  const idxOf = (seq: number): number => messages.findIndex((m) => m.seq === seq);
  // 行级滚动锚点：frozen = { seq, cut }。cut 是该消息内的行偏移（含 margin 行：0=margin 行，
  // 1=首行内容，…，totalOf-1=末行内容）。锚点可表示每一条真实渲染行（含消息间距），
  // 跨消息边界滚动时不会"免费跳过"margin 行，从而逐行精确。
  const [frozen, setFrozen] = useState<{ seq: number; cut: number } | null>(null);
  const anchorTopRow = (f: { seq: number; cut: number }): number => {
    const idx = idxOf(f.seq);
    if (idx === -1) return maxTop;
    return Math.max(0, Math.min(maxTop, rowStart[idx] + f.cut));
  };
  const anchorAtRow = (topRow: number): { seq: number; cut: number } => {
    let idx = 0;
    while (idx < messages.length - 1 && rowStart[idx + 1] <= topRow) idx++;
    const cut = Math.max(0, Math.min(totalOf(messages[idx]) - 1, topRow - rowStart[idx]));
    return { seq: messages[idx].seq, cut };
  };
  const topRow = frozen === null ? maxTop : anchorTopRow(frozen);
  const scrolledRows = Math.max(0, maxTop - topRow);
  const keyStep = Math.max(6, Math.round(viewH / 3));

  // 可视窗口：topRow 所在的消息为顶部边界（可能被裁剪），向下到填满 viewH。
  let m0 = 0;
  let clipH = 0;
  let winEnd = messages.length - 1;
  if (messages.length > 0) {
    while (m0 < messages.length - 1 && rowStart[m0 + 1] <= topRow) m0++;
    const T0 = totalOf(messages[m0]);
    clipH = Math.max(1, T0 - (topRow - rowStart[m0]));
    while (winEnd > m0 && rowStart[winEnd] >= topRow + viewH) winEnd--;
  }
  const dialogOpen = dialog !== null || approvals.length > 0;

  const showToast = useCallback((text: string, kind: "info" | "error" | "warning" = "info") => {
    setToast({ text, kind });
    if (toastTimer.current) clearTimeout(toastTimer.current);
    toastTimer.current = setTimeout(() => setToast(null), 3500);
  }, []);

  const resolveApproval = useCallback(
    (reqId: string, decision: "approved" | "denied") => {
      if (approvalBusyRef.current.has(reqId)) return;
      approvalBusyRef.current.add(reqId);
      void api
        .approval(sid, reqId, decision)
        .then(() => setApprovals((prev) => prev.filter((a) => a.id !== reqId)))
        .catch((err) => showToast(`approval failed: ${err.message}`, "error"))
        .finally(() => approvalBusyRef.current.delete(reqId));
    },
    [sid, showToast],
  );

  function upsertMessage(m: Message) {
    setMessages((prev) => {
      const i = prev.findIndex((p) => p.seq === m.seq);
      if (i === -1) return [...prev, m];
      // 同 seq 且内容未变化（重复订阅/重连的重复推送）→ 返回原数组，避免无谓重绘
      const old = prev[i];
      if (old && old.id === m.id && old.role === m.role) {
        // 工具结果会在同一条消息内持续更新，不能只比较 tools.length。
        const same = JSON.stringify(old.content ?? {}) === JSON.stringify(m.content ?? {});
        if (same) return prev;
      }
      const next = prev.slice();
      next[i] = m;
      return next;
    });
  }

  function loadSession(id: string) {
    const generation = ++loadGenerationRef.current;
    setSid(id);
    setMessages([]);
    setFrozen(null);
    heightsRef.current.clear();
    setStatus("idle");
    setEscArmed(false);
    pendingRef.current = false;
    stopRef.current?.();
    stopRef.current = null;
    setElapsed(0);
    setApprovals([]);
    oldestSeqRef.current = 0;
    onSessionChange?.(id);
    api
      .messages(id, 0, 200)
      .then((msgs) => {
        if (loadGenerationRef.current !== generation) return;
        setMessages(msgs);
        lastSeqRef.current = msgs.length ? msgs[msgs.length - 1].seq : 0;
        oldestSeqRef.current = msgs.length ? msgs[0].seq : 0;
        attachLiveEvents(id);
      })
      .catch((err) => {
        if (loadGenerationRef.current === generation) {
          showToast(`加载消息失败：${(err as Error).message}`, "error");
        }
      });
    api
      .getSession(id)
      .then((s) => {
        if (loadGenerationRef.current !== generation) return;
        setCwd(s.cwd);
        setModel(s.model || "");
        setProvider(s.provider || "");
      })
      .catch((err) => {
        if (loadGenerationRef.current === generation) showToast(`加载会话失败：${err.message}`, "error");
      });
  }

  const send = useCallback(
    (text: string) => {
      const trimmed = text.trim();
      if (trimmed === "exit" || trimmed === "quit" || trimmed === ":q") {
        onExit();
        return;
      }
      if (trimmed.startsWith("!") && trimmed.length > 1) {
        runBang(sshTarget, trimmed.slice(1).trim(), showToast);
        return;
      }
      if (trimmed.startsWith("/")) {
        const parts = trimmed.slice(1).split(/\s+/);
        const cmd = parts[0].toLowerCase();
        const arg = parts.slice(1).join(" ");
        const found = SLASH_COMMANDS.find((c) => c.name === cmd || c.aliases.includes(cmd));
        switch (found?.name) {
          case "new":
            onNew();
            break;
          case "sessions":
            setDialog({ kind: "switch" });
            break;
          case "status":
            void (async () => {
              let version = "";
              try {
                const v = await api.version();
                version = v.version || "";
              } catch {}
              const model = modelName();
              showToast(
                [
                  version ? `server: v${version}` : "server: -",
                  `session: ${sid.slice(0, 8)} · status: ${status}`,
                  `cwd: ${cwd}`,
                  `mode: ${mode} · model: ${model}`,
                  `messages: ${messages.length}`,
                ].join("\n"),
                "info",
              );
            })();
            break;
          case "models":
            void (async () => {
              try {
                const { models } = await api.models();
                if (!models.length) {
                  showToast("服务端未配置模型", "warning");
                  return;
                }
                setModelOptions(
                  models.map((m) => ({
                    key: m.model,
                    title: m.model,
                    detail: m.provider || "默认",
                  })),
                );
                setDialog({ kind: "models" });
              } catch (err) {
                showToast((err as Error).message, "error");
              }
            })();
            break;
          case "agents":
            setDialog({ kind: "agents" });
            break;
          case "init":
            showToast(`会话 cwd: ${cwd}（tars 会话即项目，无独立初始化）`, "info");
            break;
          case "themes":
            showToast("仅支持当前主题", "warning");
            break;
          case "skills":
          case "mcps":
            void (async () => {
              try {
                const tools = await api.mcpTools();
                setTools(
                  tools.map((t) => ({
                    key: t.name,
                    title: t.name,
                    detail: (t.description ?? "").slice(0, 80),
                  })),
                );
                setDialog({ kind: "tools" });
              } catch (err) {
                showToast(`获取工具列表失败：${(err as Error).message}`, "error");
              }
            })();
            break;
          case "variants":
            showToast("暂不支持 variants", "warning");
            break;
          case "ssh":
            runSsh(sshTarget, showToast);
            break;
          case "vim":
            void runVim(sshTarget, arg, showToast);
            break;
          case "copy":
            void (async () => {
              const ok = await writeClipboard(transcriptLines(messages));
              showToast(ok ? "transcript copied" : "no clipboard tool found (wl-copy/xclip)", ok ? "info" : "error");
            })();
            break;
          case "export":
            void (async () => {
              const file = path.join(process.cwd(), `tars-session-${sid.slice(0, 8)}.md`);
              try {
                await writeFile(file, transcriptLines(messages));
                showToast(`exported to ${file}`);
              } catch (err) {
                showToast((err as Error).message, "error");
              }
            })();
            break;
          case "rollback":
            void (async () => {
              try {
                const r = await api.rollback(sid);
                showToast(r.rolled_back ? "rolled back last system write" : "nothing to roll back");
                loadSession(sid);
              } catch (err) {
                showToast((err as Error).message, "error");
              }
            })();
            break;
          case "delete":
            setDialog({ kind: "confirmDelete" });
            break;
          case "editor":
            void openEditor("").then((content) => {
              if (content) promptApiRef.current?.setText(content);
            });
            break;
          case "help":
            setDialog({ kind: "help" });
            break;
          case "exit":
            onExit();
            break;
          default:
            showToast(`unknown command: /${cmd}`, "error");
        }
        return;
      }
      if (status === "running") {
        showToast("already running — wait or press esc twice to interrupt", "warning");
        return;
      }
      sendPrompt(text);
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [status, messages, sid, mode, cwd, sshTarget, showToast],
  );

  // 订阅会话事件流（跨客户端同步 + 进行中 turn 实时更新）。
  // 用 lastSeqRef 作为 after，只接收新消息；sidArg 显式传入避免闭包陈旧。
  function attachLiveEvents(sidArg: string) {
    stopRef.current?.();
    const after = lastSeqRef.current;
    const stop = streamEvents(
      api.eventURL(sidArg, after),
      api.key,
      (ev: EventData) => {
        if (ev.type === "message.created") {
          const m = ev.data as Message;
          if (m.seq === undefined) m.seq = ev.seq ?? 0;
          upsertMessage(m);
          if (m.seq > lastSeqRef.current) lastSeqRef.current = m.seq;
        } else if (ev.type === "approval.requested") {
          const a = ev.data as { id: string; action: string; resource: string };
          setApprovals((prev) => (prev.some((x) => x.id === a.id) ? prev : [...prev, { id: a.id, action: a.action, resource: a.resource }]));
        } else if (ev.type === "turn.done" || ev.type === "turn.failed") {
          if (ev.type === "turn.failed") {
            const d = ev.data as { error?: string };
            showToast(`turn failed: ${d.error ?? ""}`, "error");
          }
          pendingRef.current = false;
          stop();
          stopRef.current = null;
          setStatus("idle");
          setEscArmed(false);
        }
      },
      (err) => {
        pendingRef.current = false;
        stopRef.current = null;
        showToast(`event error: ${err.message}`, "error");
        setStatus("idle");
      },
    );
    stopRef.current = stop;
  }

  function sendPrompt(text: string) {
    pendingRef.current = true;
    setStatus("running");
    setElapsed(0);
    setFrozen(null);
    api
      .prompt(sid, text, undefined, modeRef.current)
      .then(() => {
        attachLiveEvents(sid);
      })
      .catch((err) => {
        pendingRef.current = false;
        showToast(`prompt error: ${err.message}`, "error");
        setStatus("idle");
      });
  }

  function interrupt() {
    if (status !== "running") return;
    api.interrupt(sid).catch(() => {});
    showToast("interrupt sent");
    setEscArmed(false);
  }

  // 轮询兜底
  useEffect(() => {
    const t = setInterval(() => {
      if (!pendingRef.current) return;
      api
        .getSession(sid)
        .then((s) => {
          if (s.status !== "running") {
            pendingRef.current = false;
            setStatus("idle");
            setEscArmed(false);
          }
        })
        .catch(() => {});
    }, 1000);
    return () => clearInterval(t);
  }, [sid]);

  // 上拉加载更多历史：滚动到顶部且还有更早消息时，分页拉取并前插
  useEffect(() => {
    if (loadingMoreRef.current) return;
    if (topRow > 1) return;
    if (oldestSeqRef.current <= 1) return;
    loadingMoreRef.current = true;
    api
      .messages(sid, 0, 50, oldestSeqRef.current)
      .then((older) => {
        if (older.length === 0) {
          oldestSeqRef.current = 1;
          return;
        }
        setMessages((prev) => {
          const existing = new Set(prev.map((m) => m.seq));
          const add = older.filter((m) => !existing.has(m.seq));
          return [...add, ...prev];
        });
        oldestSeqRef.current = older[0].seq;
        // 前插使 rowStart 整体后移，锚点（seq, cut）自动换算到新坐标，视口保持稳定。
      })
      .catch(() => {})
      .finally(() => {
        loadingMoreRef.current = false;
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [topRow, totalH, sid, messages.length]);

  // 运行计时
  useEffect(() => {
    if (status !== "running") return;
    const t = setInterval(() => setElapsed((e) => e + 1), 1000);
    return () => clearInterval(t);
  }, [status]);

  // 初始加载 cwd/model + 消息 + 发送初始 prompt
  useEffect(() => {
    const generation = ++loadGenerationRef.current;
    let active = true;
    api
      .getSession(sid)
      .then((s) => {
        if (!active || loadGenerationRef.current !== generation) return;
        setCwd(s.cwd);
        setModel(s.model || "");
        setProvider(s.provider || "");
      })
      .catch((err) => {
        if (active && loadGenerationRef.current === generation) {
          showToast(`加载会话失败：${(err as Error).message}`, "error");
        }
      });
    api
      .messages(sid, 0, 200)
      .then((msgs) => {
        if (!active || loadGenerationRef.current !== generation) return;
        setMessages(msgs);
        lastSeqRef.current = msgs.length ? msgs[msgs.length - 1].seq : 0;
        oldestSeqRef.current = msgs.length ? msgs[0].seq : 0;
        attachLiveEvents(sid);
      })
      .catch((err) => {
        if (active && loadGenerationRef.current === generation) {
          showToast(`加载消息失败：${(err as Error).message}`, "error");
        }
      });
    if (initialPrompt && !sentInitial.current) {
      sentInitial.current = true;
      send(initialPrompt);
    }
    // 卸载时停止事件订阅
    return () => {
      active = false;
      loadGenerationRef.current++;
      stopRef.current?.();
      stopRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 启用终端鼠标报告模式（SGR），供点击展开折叠的工具面板
  useEffect(() => {
    const out = stdout;
    out.write("\x1b[?1000h\x1b[?1006h");
    return () => {
      out.write("\x1b[?1000l\x1b[?1006l");
    };
  }, [stdout]);

  // 折叠面板展开/收起 + 区域注册
  const toggleTool = useCallback((id: string) => {
    setExpandedTools((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);
  const registerTool = useCallback((id: string, region: ToolRegion | null) => {
    setToolRegions((prev) => {
      const old = prev.get(id);
      const same = region && old
        ? old.x === region.x && old.y === region.y && old.width === region.width && old.height === region.height
        : old === region;
      if (same) return prev;
      const next = new Map(prev);
      if (region) next.set(id, region);
      else next.delete(id);
      return next;
    });
  }, []);

  // leader 超时
  useEffect(() => {
    if (!leaderPending) return;
    const t = setTimeout(() => setLeaderPending(false), 2000);
    return () => clearTimeout(t);
  }, [leaderPending]);

  // esc 双击重置
  useEffect(() => {
    if (!escArmed) return;
    const t = setTimeout(() => setEscArmed(false), 5000);
    return () => clearTimeout(t);
  }, [escArmed]);

  const leaderItems: LeaderItem[] = [
    { key: "n", label: "new session", run: onNew },
    {
      key: "l",
      label: "sessions",
      run: () => setDialog({ kind: "switch" }),
    },
    {
      key: "r",
      label: "sessions",
      run: () => setDialog({ kind: "switch" }),
    },
    {
      key: "e",
      label: "editor",
      run: () => {
        void openEditor("").then((content) => {
          if (content) promptApiRef.current?.setText(content);
        });
      },
    },
    {
      key: "y",
      label: "copy",
      run: () => {
        void writeClipboard(transcriptLines(messages)).then((ok) =>
          showToast(ok ? "transcript copied" : "no clipboard tool found", ok ? "info" : "error"),
        );
      },
    },
    { key: "h", label: "help", run: () => setDialog({ kind: "help" }) },
    { key: "q", label: "exit", run: onExit },
  ];

  // 行级平滑滚动。frozen = { seq, cut } 锚定视口顶部内容，滚轮/PageUp/Down 按行步进。
  // topRow = rowStart + 1 + cut（越大越靠下/新）。上滚→topRow 减小（看更早），下滚→增大（看更新）。
  // 上滚时冻结（避免新消息把用户拉回底部）；滚回底部后解除冻结自动跟随。
  const stepBack = (f: { seq: number; cut: number }, n: number): { seq: number; cut: number } => {
    let idx = idxOf(f.seq);
    if (idx === -1) idx = 0;
    let c = f.cut - n;
    while (idx > 0 && c < 0) {
      idx--;
      c += totalOf(messages[idx]);
    }
    return { seq: messages[idx].seq, cut: Math.max(0, c) };
  };
  const stepForward = (f: { seq: number; cut: number }, n: number): { seq: number; cut: number } => {
    let idx = idxOf(f.seq);
    if (idx === -1) idx = messages.length - 1;
    let c = f.cut + n;
    while (idx < messages.length - 1 && c >= totalOf(messages[idx])) {
      c -= totalOf(messages[idx]);
      idx++;
    }
    return { seq: messages[idx].seq, cut: Math.min(totalOf(messages[idx]) - 1, c) };
  };
  const scrollUp = (n: number) => {
    setFrozen((prev) => {
      const base = prev === null ? anchorAtRow(maxTop) : prev;
      return stepBack(base, n);
    });
  };
  const scrollDown = (n: number) => {
    setFrozen((prev) => {
      if (prev === null) return null;
      const next = stepForward(prev, n);
      return anchorTopRow(next) >= maxTop ? null : next;
    });
  };

  useInput((input, key) => {
    // 鼠标事件（ink 把 SGR 鼠标序列剥去 ESC 后作为 input 传入，形如 `[<0;x;yM`）。
    // 按钮 0=左键点击（切换折叠面板），64=滚轮上滚，65=滚轮下滚。
    const mouse = parseMouseSeq(input);
    if (mouse) {
      if (dialogOpen) return;
      if (mouse.pressed) {
        if (mouse.button === 0) {
          const x = mouse.x - 1; // 终端坐标为 1-based，layout 坐标为 0-based
          const y = mouse.y - 1; // 屏幕 1-based → layout 0-based
          for (const [id, r] of toolRegions) {
            if (x >= r.x && x < r.x + r.width && y >= r.y && y < r.y + r.height) {
              setExpandedTools((prev) => {
                const next = new Set(prev);
                next.has(id) ? next.delete(id) : next.add(id);
                return next;
              });
              break;
            }
          }
        } else if (mouse.button === 64) {
          scrollUp(WHEEL_STEP);
        } else if (mouse.button === 65) {
          scrollDown(WHEEL_STEP);
        }
      }
      return;
    }
    if (dialogOpen) return;
    if (leaderPending) {
      const items = leaderItems;
      const close = () => {
        setLeaderPending(false);
        setLeaderFocus(0);
      };
      if (key.escape) return close();
      if (key.upArrow) {
        setLeaderFocus((f) => (f - 1 + items.length) % items.length);
        return;
      }
      if (key.downArrow || key.tab) {
        setLeaderFocus((f) => (f + 1) % items.length);
        return;
      }
      if (key.return) {
        items[leaderFocus]?.run();
        return close();
      }
      const c = input.toLowerCase();
      const idx = items.findIndex((i) => i.key === c);
      if (idx !== -1) {
        items[idx].run();
        return close();
      }
      return;
    }
    if (key.ctrl && input === "x") {
      setLeaderPending(true);
      setLeaderFocus(0);
      return;
    }
    if (key.pageUp) {
      scrollUp(keyStep);
      return;
    }
    if (key.pageDown) {
      scrollDown(keyStep);
      return;
    }
  });

  const showScrollbar = totalH > viewH;
  let thumbH = 0;
  let thumbTop = 0;
  if (showScrollbar) {
    thumbH = Math.max(1, Math.round((viewH / totalH) * viewH));
    const frac = maxTop > 0 ? Math.min(1, topRow / maxTop) : 0;
    thumbTop = Math.round(frac * (viewH - thumbH));
  }

  const winMsgs = messages.slice(m0, winEnd + 1);

  return (
    <Box flexDirection="column" height="100%" paddingLeft={2} paddingRight={2}>
        <Box flexGrow={1} flexDirection="row" minHeight={0}>
        <Box
          flexGrow={1}
          flexDirection="column"
          justifyContent={totalH < viewH ? "flex-end" : "flex-start"}
          minHeight={0}
          overflowY="hidden"
        >
          {totalH > viewH && topRow <= 1 ? <Text color={theme.textMuted}>— 已到顶部 —</Text> : null}
          {messages.length > 0 ? (
            <Box height={clipH} overflowY="hidden" flexDirection="column" justifyContent="flex-end">
              <MeasuredBox seq={messages[m0].seq} onMeasured={noteHeight}>
                <MessageView
                  m={messages[m0]}
                  model={model}
                  last={m0 === messages.length - 1}
                  running={status === "running" && m0 === messages.length - 1}
                  elapsed={elapsed}
                  expandedTools={expandedTools}
                  toggleTool={toggleTool}
                  registerTool={registerTool}
                />
              </MeasuredBox>
            </Box>
          ) : null}
          {winMsgs.slice(1).map((m) => (
            <MeasuredBox key={m.seq} seq={m.seq} onMeasured={noteHeight}>
              <MessageView
                m={m}
                model={model}
                last={m.seq === messages[messages.length - 1]?.seq}
                running={status === "running" && m.seq === messages[messages.length - 1]?.seq}
                elapsed={elapsed}
                expandedTools={expandedTools}
                toggleTool={toggleTool}
                registerTool={registerTool}
              />
            </MeasuredBox>
          ))}
          {topRow < maxTop ? (
            <Text color={theme.textMuted}>↑ 已上滚 {scrolledRows} 行 · 滚轮下滚/PageDown 回到底部</Text>
          ) : null}
        </Box>
        {showScrollbar ? (
          <Box width={1} flexDirection="column" marginLeft={1}>
            {Array.from({ length: viewH }).map((_, i) => (
              <Text key={i} color={i >= thumbTop && i < thumbTop + thumbH ? theme.textMuted : theme.dim}>
                │
              </Text>
            ))}
          </Box>
        ) : null}
      </Box>

{leaderPending ? <LeaderMenu items={leaderItems} focus={leaderFocus} onClose={() => setLeaderPending(false)} /> : null}
      <Prompt
        running={status === "running"}
        elapsed={elapsed}
        escArmed={escArmed}
        model={model}
        provider={provider}
        mode={mode}
        serverUser={serverUser}
        serverIp={serverIp}
        sessionInfo={`session id: ${sid.slice(0, 8)} · ${messages.length} msgs`}
        tokens={estimateTokens(messages)}
        everTyped={everTyped}
        leaderActive={leaderPending}
        leaderHint="n=new · l/r=sessions · e=editor · q=exit · y=copy · h=help"
        inputLocked={dialogOpen}
        onSubmit={send}
        onExit={onExit}
        onInterrupt={() => {
          if (status !== "running") return;
          if (escArmed) interrupt();
          else {
            setEscArmed(true);
            showToast("press esc again to interrupt");
          }
        }}
        onToggleMode={onToggleMode}
        onTyped={onTyped}
        onToast={showToast}
        onSessionSwitch={() => setDialog({ kind: "switch" })}
        registerPrompt={(api2) => {
          promptApiRef.current = api2;
        }}
      />

      {dialog?.kind === "switch" ? (
        <SessionSwitcher
          api={api}
          currentId={sid}
          onSelect={(id) => {
            setDialog(null);
            onSessionChange(id);
          }}
          onCancel={() => setDialog(null)}
        />
      ) : null}

      {dialog?.kind === "tools" ? (
        <DialogSelect
          title="Tools (MCP)"
          options={tools}
          footerHint="↑↓ navigate · enter close · esc close"
          onSelect={() => setDialog(null)}
          onCancel={() => setDialog(null)}
        />
      ) : null}

      {dialog?.kind === "models" ? (
        <DialogSelect
          title="Models"
          options={modelOptions}
          onSelect={(o) => {
            setDialog(null);
            void api
              .updateSession(sid, o.key)
              .then(() => {
                setModel(o.key);
                return api.getSession(sid);
              })
              .then((s) => {
                if (s.provider) setProvider(s.provider);
                showToast(`model → ${o.key}`);
              })
              .catch((err) => showToast((err as Error).message, "error"));
          }}
          onCancel={() => setDialog(null)}
        />
      ) : null}
      {dialog?.kind === "agents" ? (
        <DialogSelect
          title="Agents"
          options={[
            { key: "build", title: "Build", detail: "执行工具" },
            { key: "plan", title: "Plan", detail: "只读规划" },
          ]}
          onSelect={(o) => {
            setDialog(null);
            onSetMode(o.key as "build" | "plan");
            showToast(`agent → ${o.key}`);
          }}
          onCancel={() => setDialog(null)}
        />
      ) : null}
      {dialog?.kind === "confirmDelete" ? (
        <DialogConfirm
          title="Delete session"
          message={`Delete this session (${sid.slice(0, 8)})? This cannot be undone.`}
          confirmLabel="delete"
          cancelLabel="cancel"
          onConfirm={() => {
            setDialog(null);
            void (async () => {
              try {
                await api.deleteSession(sid);
                showToast(`deleted ${sid.slice(0, 8)}`);
              } catch (err) {
                showToast((err as Error).message, "error");
              }
              onNew();
            })();
          }}
          onCancel={() => setDialog(null)}
        />
      ) : null}
      {dialog?.kind === "help" ? <DialogHelp onClose={() => setDialog(null)} /> : null}

      {approvals.length > 0 ? (
        <DialogApproval
          action={approvals[0].action}
          resource={approvals[0].resource}
          onApprove={() => resolveApproval(approvals[0].id, "approved")}
          onDeny={() => resolveApproval(approvals[0].id, "denied")}
        />
      ) : null}

      {toast ? (
        <Box position="absolute" top={1} left={0} right={0} justifyContent="center" alignItems="center">
          <Box flexDirection="column" borderStyle="single" borderColor={theme.border} backgroundColor={theme.backgroundPanel}>
            <Box paddingLeft={1} paddingRight={1} paddingTop={1} paddingBottom={1}>
              <Text
                color={toast.kind === "error" ? theme.error : toast.kind === "warning" ? theme.warning : theme.text}
                wrap="wrap"
              >
                {toast.text}
              </Text>
            </Box>
          </Box>
        </Box>
      ) : null}
    </Box>
  );
}
