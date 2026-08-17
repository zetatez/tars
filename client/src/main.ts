#!/usr/bin/env node
import { createHmac } from "node:crypto";
import { createElement } from "react";
import { API } from "./api.js";
import { describeTool, formatMsgTime } from "./format.js";
import { streamEvents } from "./sse.js";
import type { EventData, Message, ToolCall } from "./types.js";

const BASE_URL = "http://localhost:8899";

function parseGlobalArgs(argv: string[]): { baseURL: string; key: string; clientUser: string; passphrase: string; rest: string[] } {
  let baseURL = process.env.TARS_BASE_URL ?? BASE_URL;
  let key = process.env.TARS_API_KEY ?? "";
  let passphrase = process.env.TARS_ADMIN_PASSPHRASE ?? "";
  let clientUser = process.env.TARS_SSH_USER ?? process.env.USER ?? "";
  const rest: string[] = [];
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--base-url" || a === "-u") baseURL = argv[++i] ?? baseURL;
    else if (a === "--key" || a === "-k") key = argv[++i] ?? key;
    else if (a === "--passphrase" || a === "-P") passphrase = argv[++i] ?? passphrase;
    else if (a === "--client-user") clientUser = argv[++i] ?? clientUser;
    else if (a === "--ssh-user") clientUser = argv[++i] ?? clientUser;
    else rest.push(a);
  }
  return { baseURL, key, clientUser, passphrase, rest };
}

function fail(msg: string): never {
  console.error(`error: ${msg}`);
  process.exit(1);
}

// 与服务端一致的 admin key 派生规则：
// secret = HMAC-SHA256(passphrase, machineID)；key_id = "tars-admin-" + HMAC(passphrase,"keyid:"+machineID)[:12]
function deriveAdminKey(passphrase: string, machineID: string): string {
  const secret = createHmac("sha256", passphrase).update(machineID).digest("hex");
  const keyID = "tars-admin-" + createHmac("sha256", passphrase).update("keyid:" + machineID).digest("hex").slice(0, 12);
  return `${keyID}_${secret}`;
}

function prettyJSON(v: unknown): string {
  return JSON.stringify(v, null, 2);
}

const C = {
  gray: "\x1b[90m",
  cyan: "\x1b[36m",
  green: "\x1b[32m",
  yellow: "\x1b[33m",
  red: "\x1b[31m",
  blue: "\x1b[34m",
  reset: "\x1b[0m",
};

const KIND_COLOR: Record<string, string> = {
  cmd: C.cyan,
  info: C.gray,
  out: C.gray,
  err: C.red,
  link: C.blue,
};

function renderTool(t: ToolCall): void {
  const d = describeTool(t);
  const head =
    d.state === "ok"
      ? `${C.green}ok${C.reset}`
      : d.state === "error"
        ? `${C.red}error${C.reset}`
        : d.state === "rejected"
          ? `${C.yellow}rejected${C.reset}`
          : `${C.gray}${t.status ?? "?"}${C.reset}`;
  console.log(`${C.gray}[${d.name}${C.reset} ${head}${C.gray}]${C.reset}`);
  for (const l of d.lines) {
    for (const s of l.text.split("\n")) {
      console.log(`${C.gray}  ${C.reset}${KIND_COLOR[l.kind] ?? C.gray}${s}${C.reset}`);
    }
  }
}

function renderMessage(m: Message): void {
  const c = m.content;
  const text = c.text ?? "";
  const ts = m.created ? ` ${C.gray}[${formatMsgTime(m.created)}]${C.reset}` : "";
  if (m.role === "user") {
    console.log(`${C.cyan}[user]${C.reset}${ts} ${text}`);
    return;
  }
  if (c.error) {
    console.log(`${C.red}[assistant-error]${C.reset}${ts} ${c.error}`);
    return;
  }
  if (c.tools && c.tools.length > 0) {
    for (const t of c.tools) renderTool(t);
  }
  if (text) console.log(`${C.green}[assistant]${C.reset}${ts} ${text}`);
}

async function cmdPrompt(api: API, args: string[]): Promise<void> {
  const [id, ...rest] = args;
  const text = rest.join(" ");
  if (!id || !text) fail("usage: prompt <sessionId> <text...>");
  const ik = `cli-${Date.now()}`;

  // 发送前记录当前最后 seq（之后只订阅新增）
  let after = 0;
  try {
    const msgs = await api.messages(id, 0, 200);
    after = msgs.length ? msgs[msgs.length - 1].seq : 0;
  } catch {
    /* 新 session 无消息 */
  }

  const { turnId } = await api.prompt(id, text, ik);
  console.log(`${C.gray}turn: ${turnId}${C.reset}`);
  console.log(`${C.cyan}[user]${C.reset} ${text}`);
  const t0 = Date.now();

  await new Promise<void>((resolve) => {
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      stop();
      console.log(`${C.gray}(done in ${((Date.now() - t0) / 1000).toFixed(1)}s)${C.reset}`);
      resolve();
    };
    const stop = streamEvents(
      api.eventURL(id, after),
      apiKeyOf(api),
      (ev: EventData) => {
        switch (ev.type) {
          case "message.created": {
            const m = ev.data as Message;
            // 用户消息已在发送时立即打印，跳过重复
            if (m.role === "user") break;
            renderMessage(m);
            break;
          }
          case "approval.requested": {
            const a = ev.data as { id: string; action: string; resource: string };
            console.log(`${C.yellow}[审批]${C.reset} ${a.action}: ${a.resource} (id=${a.id})  → approval approve/reject <sessionId> <approvalId>`);
            break;
          }
          case "turn.done":
          case "turn.failed":
            if (ev.type === "turn.failed") {
              const d = ev.data as { error?: string };
              console.log(`${C.red}[turn failed]${C.reset} ${d.error ?? ""}`);
            }
            finish();
            break;
        }
      },
      (err) => {
        console.error(`\x1b[31m[event error]\x1b[0m ${err.message}`);
        finish();
      },
    );
    // 兜底：turn 若在订阅建立前已结束（快 LLM），live 事件会丢失；
    // 轮询 session 状态，非 running 即认为完成。
    const timer = setInterval(async () => {
      try {
        const s = await api.getSession(id);
        if (s.status !== "running") {
          clearInterval(timer);
          finish();
        }
      } catch {
        /* ignore */
      }
    }, 500);
  });
  // 结束 SSE 订阅后退出（避免后台连接挂住事件循环）
  process.exit(0);
}

async function cmdSession(api: API, args: string[]): Promise<void> {
  const sub = args[0];
  switch (sub) {
    case "create": {
      const cwd = flag(args, "--cwd");
      const model = flag(args, "--model");
      const pm = flag(args, "--prompt-mode");
      const s = await api.createSession(cwd, model, pm);
      console.log(`created session ${s.id}  cwd=${s.cwd}  model=${s.model || "(default)"}`);
      break;
    }
    case "list": {
      const { sessions } = await api.listSessions();
      if (sessions.length === 0) return console.log("(no sessions)");
      for (const s of sessions) {
        console.log(`${s.id}  ${s.status.padEnd(10)} ${s.cwd}  model=${s.model || "-"}`);
      }
      break;
    }
    case "show": {
      const id = args[1];
      if (!id) fail("usage: session show <id>");
      console.log(prettyJSON(await api.getSession(id)));
      break;
    }
    case "delete": {
      const id = args[1];
      if (!id) fail("usage: session delete <id>");
      await api.deleteSession(id);
      console.log(`deleted ${id}`);
      break;
    }
    default:
      fail("usage: session create|list|show|delete");
  }
}

async function cmdMessages(api: API, args: string[]): Promise<void> {
  const [id] = args;
  if (!id) fail("usage: messages <sessionId>");
  const after = parseInt(flag(args, "--after") ?? "0", 10) || 0;
  const msgs = await api.messages(id, after);
  if (msgs.length === 0) return console.log("(no messages)");
  for (const m of msgs) renderMessage(m);
}

async function cmdEvent(api: API, args: string[]): Promise<void> {
  const [id] = args;
  if (!id) fail("usage: event <sessionId>");
  const stop = streamEvents(api.eventURL(id), apiKeyOf(api), (ev) => {
    if (ev.type === "message.created") {
      renderMessage(ev.data as Message);
    } else {
      console.log(`\x1b[90m[${ev.type}]\x1b[0m ${ev.data ? JSON.stringify(ev.data) : ""}`);
    }
  });
  console.log("(订阅中，Ctrl-C 退出)");
  process.on("SIGINT", () => {
    stop();
    process.exit(0);
  });
}

async function cmdKeys(api: API, args: string[]): Promise<void> {
  const sub = args[0];
  if (sub === "create") {
    const label = flag(args, "--label");
    const k = await api.createKey(label);
    console.log(`key_id: ${k.key_id}\nkey:    ${k.key}\n(label: ${label ?? ""})`);
  } else if (sub === "revoke") {
    const id = args[1];
    if (!id) fail("usage: keys revoke <keyId>");
    await api.revokeKey(id);
    console.log(`revoked ${id}`);
  } else {
    fail("usage: keys create|revoke");
  }
}

async function cmdConfig(api: API, args: string[]): Promise<void> {
  const [keyId, sub, ...kv] = args;
  if (!keyId) fail("usage: config <keyId> get|set k=v...");
  if (sub === "get") {
    console.log(prettyJSON(await api.getConfig(keyId)));
  } else if (sub === "set") {
    const cfg: Record<string, unknown> = {};
    for (const item of kv) {
      const eq = item.indexOf("=");
      if (eq <= 0) fail(`bad k=v: ${item}`);
      cfg[item.slice(0, eq)] = item.slice(eq + 1);
    }
    console.log(prettyJSON(await api.setConfig(keyId, cfg)));
  } else {
    fail("usage: config <keyId> get|set k=v...");
  }
}

function flag(args: string[], name: string): string | undefined {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
}

function apiKeyOf(api: API): string {
  return api.key;
}

async function cmdTui(api: API, args: string[]): Promise<void> {
  if (!process.stdout.isTTY) fail("TUI 需要交互式终端（tty）");
  let sid = flag(args, "--session");
  let initialMessages: Message[] | undefined;
  let initialPrompt: string | undefined;
  if (sid) {
    initialMessages = await api.messages(sid, 0, 200);
  } else if (args.includes("--continue")) {
    // 用 globalSessions（按 time_updated DESC 排序），优先恢复自己的最近会话
    const { sessions } = await api.globalSessions(20, 0);
    const last = sessions.find((s) => s.mine) ?? sessions[0];
    if (last) {
      sid = last.id;
      initialMessages = await api.messages(sid, 0, 200);
    }
  }
  const p = flag(args, "--prompt") ?? flag(args, "-p");
  if (p && !sid) {
    const s = await api.createSession();
    sid = s.id;
    initialMessages = [];
    initialPrompt = p;
  }
  const { render } = await import("ink");
  const { TuiApp } = await import("./tui.js");
  const { sshTargetFromBase } = await import("./tui/ssh.js");
  const sshUser = flag(args, "--ssh-user") ?? process.env.TARS_SSH_USER;
  const sshPort = Number(flag(args, "--ssh-port") ?? process.env.TARS_SSH_PORT ?? "0");
  render(
    createElement(TuiApp, {
      api,
      sessionId: sid ?? undefined,
      initialMessages,
      initialPrompt,
      sshTarget: sshTargetFromBase(api.baseURL, sshUser, Number.isFinite(sshPort) ? sshPort : 0),
    }),
    { exitOnCtrlC: false },
  );
}

async function main(): Promise<void> {
  let { baseURL, key, clientUser, passphrase, rest } = parseGlobalArgs(process.argv.slice(2));
  if (!key) {
    if (!passphrase) fail("missing API key (use --key <KEY> or TARS_API_KEY)");
    // 有口令无 key：从目标服务器获取 machine-id，派生该服务器的 admin key
    const probe = new API(baseURL, "", clientUser);
    const machineID = await probe.machineID();
    key = deriveAdminKey(passphrase, machineID);
  }
  const api = new API(baseURL, key, clientUser);
  const [cmd, ...args] = rest;
  try {
    if (!cmd) {
      // 无命令：默认进入交互式 TUI
      await cmdTui(api, args);
      return;
    }
    switch (cmd) {
      case "health": {
        const h = await api.health();
        console.log(prettyJSON(h));
        break;
      }
      case "version": {
        const v = await api.version();
        console.log(prettyJSON({ ...v }));
        break;
      }
      case "session":
        await cmdSession(api, args);
        break;
      case "prompt":
        await cmdPrompt(api, args);
        break;
      case "messages":
        await cmdMessages(api, args);
        break;
      case "event":
        await cmdEvent(api, args);
        break;
      case "interrupt": {
        const id = args[0];
        if (!id) fail("usage: interrupt <sessionId>");
        await api.interrupt(id);
        console.log("interrupted");
        break;
      }
      case "rollback": {
        const id = args[0];
        if (!id) fail("usage: rollback <sessionId>");
        console.log(prettyJSON(await api.rollback(id)));
        break;
      }
      case "keys":
        await cmdKeys(api, args);
        break;
      case "config":
        await cmdConfig(api, args);
        break;
      case "stats": {
        const id = args[0];
        if (!id) fail("usage: stats <keyId>");
        console.log(prettyJSON(await api.stats(id)));
        break;
      }
      case "tui":
      case "ui":
        // tui [--session <id>] [--continue] [--prompt <text>] [--ssh-user <u>] [--ssh-port <p>]
        await cmdTui(api, args);
        break;
      default:
        fail(`unknown command: ${cmd}`);
    }
  } catch (err) {
    const msg = (err as Error).message;
    if (/fetch failed|ECONNREFUSED|connection refused/i.test(msg)) {
      fail(`无法连接 tars 服务（${baseURL}），请先启动后端或检查 --base-url`);
    }
    fail(msg);
  }
}

// 供 cmdPrompt 内部使用
main();
