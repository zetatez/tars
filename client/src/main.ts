#!/usr/bin/env node
import { API } from "./api.js";
import { streamEvents } from "./sse.js";
import type { EventData, Message } from "./types.js";

const BASE_URL = "http://localhost:8899";

function parseGlobalArgs(argv: string[]): { baseURL: string; key: string; rest: string[] } {
  let baseURL = process.env.TARS_BASE_URL ?? BASE_URL;
  let key = process.env.TARS_API_KEY ?? "";
  const rest: string[] = [];
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--base-url" || a === "-u") baseURL = argv[++i] ?? baseURL;
    else if (a === "--key" || a === "-k") key = argv[++i] ?? key;
    else rest.push(a);
  }
  return { baseURL, key, rest };
}

function fail(msg: string): never {
  console.error(`error: ${msg}`);
  process.exit(1);
}

function prettyJSON(v: unknown): string {
  return JSON.stringify(v, null, 2);
}

function renderMessage(m: Message): void {
  const c = m.content;
  const text = c.text ?? "";
  if (m.role === "user") {
    console.log(`\x1b[36m[user]\x1b[0m ${text}`);
    return;
  }
  if (c.error) {
    console.log(`\x1b[31m[assistant-error]\x1b[0m ${c.error}`);
    return;
  }
  if (c.tools && c.tools.length > 0) {
    for (const t of c.tools) {
      const r = t.result as Record<string, unknown> | undefined;
      const stdout = (r?.stdout as string) ?? "";
      const status = t.status === "ok" ? "\x1b[32mok\x1b[0m" : `\x1b[33m${t.status}\x1b[0m`;
      console.log(`\x1b[90m[${t.name} ${status}]\x1b[0m ${stdout.trim()}`);
      if (r?.rejected) console.log(`\x1b[90m  rejected: ${(r.reason as string) ?? ""}\x1b[0m`);
      if (r?.error) console.log(`\x1b[31m  ${r.error}\x1b[0m`);
    }
  }
  if (text) console.log(`\x1b[32m[assistant]\x1b[0m ${text}`);
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
  console.log(`\x1b[90mturn: ${turnId}\x1b[0m`);

  await new Promise<void>((resolve) => {
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      stop();
      resolve();
    };
    const stop = streamEvents(
      api.eventURL(id, after),
      apiKeyOf(api),
      (ev: EventData) => {
        switch (ev.type) {
          case "message.created":
            renderMessage(ev.data as Message);
            break;
          case "approval.requested": {
            const a = ev.data as { id: string; action: string; resource: string };
            console.log(`\x1b[33m[审批] ${a.action}: ${a.resource} (id=${a.id})\x1b[0m`);
            break;
          }
          case "turn.done":
          case "turn.failed":
            if (ev.type === "turn.failed") {
              const d = ev.data as { error?: string };
              console.log(`\x1b[31m[turn failed]\x1b[0m ${d.error ?? ""}`);
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
  return (api as unknown as { key: string }).key;
}

async function main(): Promise<void> {
  const { baseURL, key, rest } = parseGlobalArgs(process.argv.slice(2));
  if (!key) fail("missing API key (use --key <KEY> or TARS_API_KEY)");
  const [cmd, ...args] = rest;
  if (!cmd) fail("usage: tars-cli <command> ...  (try: health, session create, prompt <id> <text>)");

  const api = new API(baseURL, key);
  try {
    switch (cmd) {
      case "health": {
        const h = await api.health();
        console.log(prettyJSON(h));
        break;
      }
      case "version": {
        console.log(prettyJSON(await api.version()));
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
      default:
        fail(`unknown command: ${cmd}`);
    }
  } catch (err) {
    fail((err as Error).message);
  }
}

// 供 cmdPrompt 内部使用
main();
