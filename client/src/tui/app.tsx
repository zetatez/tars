import React, { useCallback, useEffect, useRef, useState } from "react";
import { Box, Text, useApp, useInput, useStdout } from "ink";
import { execFile } from "node:child_process";
import { writeFile } from "node:fs/promises";
import { hostname } from "node:os";
import { lookup } from "node:dns/promises";
import path from "node:path";

import { API } from "../api.js";
import { streamEvents } from "../sse.js";
import type { EventData, Message } from "../types.js";
import { theme, charWidth } from "./theme.js";
import { MessageView } from "./messages.js";
import { Prompt } from "./prompt.js";
import { DialogSelect, DialogConfirm, DialogHelp, DialogApproval, LeaderMenu, SessionSwitcher, type SelectOption, type LeaderItem } from "./dialog.js";
import { SLASH_COMMANDS } from "./keys.js";
import { openEditor } from "./editor.js";
import { setSuspendImpl } from "./suspend.js";
import { StatusBar } from "./statusbar.js";
import { runSsh, runVim, runBang } from "./commands.js";
import { type SshTarget } from "./ssh.js";

const PAGE = 8;

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
    lines += 8; // 折叠显示的行数上限（COLLAPSE_LINES=10，取中）
    lines += 1; // marginTop
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
  const [statusBarH, setStatusBarH] = useState<number | undefined>(undefined);
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
  const clientUser = api.clientUser ?? "";
  const serverUser = sshTarget.user ?? "";

  const [serverIp, setServerIp] = useState(host);
  const [clientIp, setClientIp] = useState(hostname());
  useEffect(() => {
    let alive = true;
    void resolveIp(host).then((ip) => alive && setServerIp(ip));
    return () => { alive = false; };
  }, [host]);
  useEffect(() => {
    let alive = true;
    void resolveIp(hostname()).then((ip) => alive && setClientIp(ip));
    return () => { alive = false; };
  }, []);

  const quit = () => {
    exit();
    setTimeout(() => process.exit(0), 100);
  };

  return (
    <Box flexDirection="column" height="100%">
      <Box flexShrink={0} paddingBottom={1}>
        <StatusBar
          currentSessionId={view.type === "session" ? view.id : undefined}
          onHeightChange={setStatusBarH}
        />
      </Box>
      <Box flexGrow={1} flexDirection="column" minHeight={0}>
        {view.type === "home" ? (
          <HomeView
            api={api}
            sshTarget={sshTarget}
            mode={mode}
            serverUser={serverUser}
            serverIp={serverIp}
            clientUser={clientUser}
            clientIp={clientIp}
            onToggleMode={toggleMode}
            onSetMode={setModeDirect}
            everTyped={everTyped}
            onTyped={markTyped}
            onStart={async (text) => {
              try {
                const s = await api.createSession();
                setPendingPrompt(text);
                setView({ type: "session", id: s.id });
              } catch {}
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
            statusBarHeight={statusBarH}
            initialMessages={initialMessages && view.id === sessionId ? initialMessages : []}
            initialPrompt={pendingPrompt}
            mode={mode}
            serverUser={serverUser}
            serverIp={serverIp}
            clientUser={clientUser}
            clientIp={clientIp}
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
  clientUser,
  clientIp,
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
  clientUser: string;
  clientIp: string;
  onToggleMode: () => void;
  onSetMode: (m: "build" | "plan") => void;
  everTyped: boolean;
  onTyped: () => void;
  onStart: (text: string) => void;
  onResume: (id: string) => void;
  onExit: () => void;
}) {
  const [leaderPending, setLeaderPending] = useState(false);
  const [dialog, setDialog] = useState<null | { kind: "sessions" } | { kind: "help" } | { kind: "models" } | { kind: "agents" } | { kind: "switch" }>(null);
  const [sessions, setSessions] = useState<SelectOption[]>([]);
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
        case "skills":
        case "variants":
        case "mcps":
          showToast(`暂不支持 /${cmd}`, "warning");
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
    onStart(text);
  };

  const openSessions = () => {
    void (async () => {
      try {
        const { sessions: list } = await api.listSessions();
        setSessions(
          list.map((s) => ({
            key: s.id,
            title: s.id.slice(0, 8),
            detail: `${s.status}`,
          })),
        );
        setDialog({ kind: "sessions" });
      } catch {}
    })();
  };

  useInput((input, key) => {
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
          clientUser={clientUser}
          clientIp={clientIp}
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
          onCommandSelect={(cmd) => handleSubmit(cmd)}
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

      {dialog?.kind === "sessions" ? (
        <DialogSelect
          title="Sessions"
          options={sessions}
          onSelect={(o) => {
            setDialog(null);
            onResume(o.key);
          }}
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
  | { kind: "sessions" }
  | { kind: "switch" }
  | { kind: "models" }
  | { kind: "agents" }
  | { kind: "confirmDelete" }
  | { kind: "help" };

function SessionView({
  api,
  sshTarget,
  sessionId,
  statusBarHeight,
  initialMessages,
  initialPrompt,
  mode,
  serverUser,
  serverIp,
  clientUser,
  clientIp,
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
  statusBarHeight?: number;
  initialMessages: Message[];
  initialPrompt?: string;
  mode: "build" | "plan";
  serverUser: string;
  serverIp: string;
  clientUser: string;
  clientIp: string;
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
  // 消息区可视高度：终端行数扣除顶部状态栏、session header、底部输入区（约 5 行）。
  const viewH = Math.max(5, rows - (statusBarHeight ?? 10) - 6);

  const [sid, setSid] = useState(sessionId);
  const [messages, setMessages] = useState<Message[]>(initialMessages);
  const [status, setStatus] = useState<"idle" | "running">("idle");
  const [elapsed, setElapsed] = useState(0);
  const [cwd, setCwd] = useState("");
  const [model, setModel] = useState("");
  const [provider, setProvider] = useState("");
  const [toast, setToast] = useState<{ text: string; kind: "info" | "error" | "warning" } | null>(null);
  const [scroll, setScroll] = useState(0);
  const [escArmed, setEscArmed] = useState(false);
  const [leaderPending, setLeaderPending] = useState(false);
  const [leaderFocus, setLeaderFocus] = useState(0);
  const [toolsExpanded, setToolsExpanded] = useState(false);
  const [dialog, setDialog] = useState<DialogState>(null);
  const [sessions, setSessions] = useState<SelectOption[]>([]);
  const [modelOptions, setModelOptions] = useState<SelectOption[]>([]);
  const [approvals, setApprovals] = useState<Array<{ id: string; action: string; resource: string }>>([]);

  const modelName = () => (provider ? `${provider}:` : "") + (model || "tars");
  const frozenRef = useRef<number | null>(null);
  const lastSeqRef = useRef(initialMessages.length ? initialMessages[initialMessages.length - 1].seq : 0);
  const pendingRef = useRef(false);
  const stopRef = useRef<(() => void) | null>(null);
  const toastTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const promptApiRef = useRef<{ setText(text: string): void } | null>(null);
  const sentInitial = useRef(!initialPrompt);
  const modeRef = useRef(mode);
  modeRef.current = mode;

  const viewBottom = frozenRef.current === null ? messages.length : frozenRef.current;
  const end = Math.max(0, viewBottom - scroll);
  // 从底部向上累计每条消息的估算行数，只保留能放进可视区域的消息。
  const contentWidth = Math.max(20, columns - 8);
  const visible: Message[] = [];
  {
    let used = 0;
    for (let i = end - 1; i >= 0; i--) {
      const lines = estimateMessageLines(messages[i], contentWidth);
      if (used + lines > viewH && visible.length > 0) break;
      visible.unshift(messages[i]);
      used += lines;
    }
  }
  const dialogOpen = dialog !== null || approvals.length > 0;

  const showToast = useCallback((text: string, kind: "info" | "error" | "warning" = "info") => {
    setToast({ text, kind });
    if (toastTimer.current) clearTimeout(toastTimer.current);
    toastTimer.current = setTimeout(() => setToast(null), 3500);
  }, []);

  const resolveApproval = useCallback(
    (reqId: string, decision: "approved" | "denied") => {
      setApprovals((prev) => prev.filter((a) => a.id !== reqId));
      void api.approval(sid, reqId, decision).catch((err) => {
        showToast(`approval failed: ${err.message}`, "error");
      });
    },
    [sid, showToast],
  );

  function upsertMessage(m: Message) {
    setMessages((prev) => {
      const i = prev.findIndex((p) => p.seq === m.seq);
      if (i === -1) return [...prev, m];
      const next = prev.slice();
      next[i] = m;
      return next;
    });
  }

  function loadSession(id: string) {
    setSid(id);
    setMessages([]);
    setScroll(0);
    frozenRef.current = null;
    setStatus("idle");
    setEscArmed(false);
    pendingRef.current = false;
    stopRef.current?.();
    stopRef.current = null;
    setElapsed(0);
    onSessionChange?.(id);
    api
      .messages(id, 0, 200)
      .then((msgs) => {
        setMessages(msgs);
        lastSeqRef.current = msgs.length ? msgs[msgs.length - 1].seq : 0;
      })
      .catch(() => {});
    api
      .getSession(id)
      .then((s) => {
        setCwd(s.cwd);
        if (s.model) setModel(s.model);
        if (s.provider) setProvider(s.provider);
      })
      .catch(() => {});
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
            void (async () => {
              try {
                const { sessions: list } = await api.listSessions();
                setSessions(
                  list.map((s) => ({
                    key: s.id,
                    title: s.id.slice(0, 8),
                    detail: `${s.status} · ${s.cwd}`,
                  })),
                );
                setDialog({ kind: "sessions" });
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
            showToast("暂不支持 skills", "warning");
            break;
          case "variants":
            showToast("暂不支持 variants", "warning");
            break;
          case "mcps":
            showToast("暂不支持 mcps", "warning");
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

  function sendPrompt(text: string) {
    pendingRef.current = true;
    setStatus("running");
    setElapsed(0);
    setScroll(0);
    frozenRef.current = null;
    const after = lastSeqRef.current;
    api
      .prompt(sid, text, undefined, modeRef.current)
      .then(() => {
        const stop = streamEvents(
          api.eventURL(sid, after),
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
              setStatus("idle");
              setEscArmed(false);
            }
          },
          (err) => {
            pendingRef.current = false;
            showToast(`event error: ${err.message}`, "error");
            setStatus("idle");
          },
        );
        stopRef.current = stop;
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

  // 运行计时
  useEffect(() => {
    if (status !== "running") return;
    const t = setInterval(() => setElapsed((e) => e + 1), 1000);
    return () => clearInterval(t);
  }, [status]);

  // 初始加载 cwd/model + 消息 + 发送初始 prompt
  useEffect(() => {
    api
      .getSession(sid)
      .then((s) => {
        setCwd(s.cwd);
        if (s.model) setModel(s.model);
        if (s.provider) setProvider(s.provider);
      })
      .catch(() => {});
    api
      .messages(sid, 0, 200)
      .then((msgs) => {
        setMessages(msgs);
        lastSeqRef.current = msgs.length ? msgs[msgs.length - 1].seq : 0;
      })
      .catch(() => {});
    if (initialPrompt && !sentInitial.current) {
      sentInitial.current = true;
      send(initialPrompt);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
      run: () => {
        void (async () => {
          try {
            const { sessions: list } = await api.listSessions();
            setSessions(
              list.map((s) => ({
                key: s.id,
                title: s.id.slice(0, 8),
                detail: `${s.status} · ${s.cwd}`,
              })),
            );
            setDialog({ kind: "sessions" });
          } catch (err) {
            showToast((err as Error).message, "error");
          }
        })();
      },
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

  useInput((input, key) => {
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
    if (key.ctrl && input === "o") {
      setToolsExpanded((e) => !e);
      return;
    }
    if (key.pageUp) {
      if (frozenRef.current === null) frozenRef.current = messages.length;
      setScroll((s) => Math.min(s + PAGE, Math.max(0, (frozenRef.current ?? messages.length) - 1)));
      return;
    }
    if (key.pageDown) {
      setScroll((s) => {
        const next = Math.max(0, s - PAGE);
        if (next === 0 && s > 0) frozenRef.current = null;
        return next;
      });
      return;
    }
  });

  const total = messages.length;
  let thumbH = 0;
  let thumbTop = 0;
  if (total > 0) {
    if (total <= viewH) {
      thumbH = viewH;
    } else {
      thumbH = Math.max(1, Math.round((viewH / total) * viewH));
      const maxScroll = Math.max(0, (frozenRef.current ?? messages.length) - 1);
      const frac = maxScroll > 0 ? Math.min(1, scroll / maxScroll) : 0;
      thumbTop = Math.round((1 - frac) * (viewH - thumbH));
    }
  }
  const showScrollbar = total > viewH;

  return (
    <Box flexDirection="column" height="100%" paddingLeft={2} paddingRight={2}>
      <Box flexGrow={1} flexDirection="row" minHeight={0}>
        <Box flexGrow={1} flexDirection="column" justifyContent="flex-end" minHeight={0} overflowY="hidden">
          {scroll > 0 && end <= 1 ? (
            <Text color={theme.textMuted}>— 已到顶部 —</Text>
          ) : null}
          {visible.map((m) => (
            <MessageView
              key={m.seq}
              m={m}
              model={model}
              last={m.role === "assistant" && m.seq === messages[messages.length - 1]?.seq}
              running={status === "running" && m.seq === messages[messages.length - 1]?.seq}
              elapsed={elapsed}
              toolsExpanded={toolsExpanded}
            />
          ))}
          {scroll > 0 ? <Text color={theme.textMuted}>↑ 已上滚 {scroll} 条 · PageDown 回到底部</Text> : null}
        </Box>
        {showScrollbar ? (
          <Box width={1} flexDirection="column" marginLeft={1}>
            {Array.from({ length: viewH }).map((_, i) => (
              <Text key={i} color={theme.border}>
                {i >= thumbTop && i < thumbTop + thumbH ? "█" : "│"}
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
        clientUser={clientUser}
        clientIp={clientIp}
        sessionInfo={`${status} · session id: ${sid.slice(0, 8)} · ${messages.length} msgs`}
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
        onCommandSelect={send}
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
            loadSession(id);
          }}
          onCancel={() => setDialog(null)}
        />
      ) : null}

      {dialog?.kind === "sessions" ? (
        <DialogSelect
          title="Sessions"
          options={sessions}
          onSelect={(o) => {
            setDialog(null);
            loadSession(o.key);
          }}
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
