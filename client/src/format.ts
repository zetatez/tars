import type { ToolCall } from "./types.js";

// 语义化行：kind 决定 CLI(ANSI) / TUI(Ink) 的着色
export type LineKind = "cmd" | "info" | "out" | "err" | "link";

export interface ToolLine {
  kind: LineKind;
  text: string;
}

export type ToolState = "ok" | "error" | "rejected" | "other";

export interface ToolDisplay {
  name: string;
  state: ToolState;
  lines: ToolLine[];
}

const safeArgv = /^[\w@%+=:,./-]+$/;

// argv 数组 → 可读的 shell 命令串（含空白的参数用双引号括起）
export function formatArgv(argv: string[]): string {
  return argv.map((a) => (safeArgv.test(a) ? a : JSON.stringify(a))).join(" ");
}

function asObj(v: unknown): Record<string, unknown> {
  return (v ?? {}) as Record<string, unknown>;
}

function kv(key: string, v: unknown): ToolLine {
  const s = typeof v === "string" ? v : JSON.stringify(v);
  return { kind: "info", text: `${key}: ${s}` };
}

function stringArgs(args: Record<string, unknown>, skip: string[]): ToolLine[] {
  const out: ToolLine[] = [];
  for (const [k, v] of Object.entries(args)) {
    if (skip.includes(k) || v === null || v === undefined || v === "") continue;
    out.push(kv(k, v));
  }
  return out;
}

// 将一次工具调用渲染为结构化展示（调用参数 + 返回结果）
export function describeTool(t: ToolCall): ToolDisplay {
  const args = asObj(t.args);
  const result = asObj(t.result);
  const lines: ToolLine[] = [];

  let state: ToolState = "other";
  if (result.denied) state = "rejected";
  else if (result.rejected) state = "rejected";
  else if (result.error) state = "error";
  else if (t.status === "ok") state = "ok";

  // ---- 调用参数 ----
  switch (t.name) {
    case "exec_command": {
      if (Array.isArray(args.argv)) {
        lines.push({ kind: "cmd", text: `$ ${formatArgv(args.argv as string[])}` });
      }
      if (typeof args.cwd === "string" && args.cwd) lines.push(kv("cwd", args.cwd));
      break;
    }
    case "webfetch":
      if (typeof args.url === "string") lines.push({ kind: "cmd", text: `GET ${args.url}` });
      break;
    case "websearch":
    case "memory_query":
    case "grep":
      lines.push(...stringArgs(args, ["path"]));
      break;
    default:
      lines.push(...stringArgs(args, ["content", "patch", "new_string", "old_string"]));
  }

  // ---- 返回结果 ----
  if (state === "rejected") {
    lines.push({ kind: "err", text: `rejected: ${result.reason ?? ""}` });
    return { name: t.name, state, lines };
  }
  if (state === "error") {
    lines.push({ kind: "err", text: `error: ${result.error}` });
    return { name: t.name, state, lines };
  }

  switch (t.name) {
    case "exec_command": {
      if (typeof result.exit === "number") {
        lines.push({ kind: result.exit === 0 ? "info" : "err", text: `exit ${result.exit}` });
      }
      if (typeof result.stderr === "string" && result.stderr.trim()) {
        lines.push({ kind: "err", text: result.stderr.trim() });
      }
      if (typeof result.stdout === "string" && result.stdout.trim()) {
        lines.push({ kind: "out", text: result.stdout.trim() });
      }
      break;
    }
    case "websearch": {
      const rs = result.results as Array<{ title?: string; url?: string }> | undefined;
      if (Array.isArray(rs)) {
        for (const r of rs) {
          if (r.title) lines.push({ kind: "out", text: `· ${r.title}` });
          if (r.url) lines.push({ kind: "link", text: `  ${r.url}` });
        }
      } else {
        lines.push({ kind: "info", text: `no results` });
      }
      break;
    }
    case "webfetch": {
      lines.push({ kind: "info", text: `HTTP ${result.status ?? "?"}` });
      if (typeof result.text === "string" && result.text.trim()) {
        lines.push({ kind: "out", text: result.text.trim() });
      }
      break;
    }
    case "read_file": {
      if (typeof result.content === "string") lines.push({ kind: "out", text: result.content });
      if (result.truncated) lines.push({ kind: "info", text: `...(截断至 ${result.size} 字节)` });
      break;
    }
    case "grep": {
      const ms = result.matches as Array<{ file?: string; line?: number; text?: string }> | undefined;
      if (Array.isArray(ms)) {
        for (const m of ms) {
          lines.push({ kind: "out", text: `${m.file}:${m.line ?? 0}: ${m.text ?? ""}` });
        }
        lines.push({ kind: "info", text: `count ${result.count ?? ms.length}` });
      }
      break;
    }
    case "ls": {
      const es = result.entries as Array<{ name?: string; is_dir?: boolean; size?: number }> | undefined;
      if (Array.isArray(es)) {
        for (const e of es) lines.push({ kind: "out", text: `${e.is_dir ? "d" : "-"} ${e.name ?? ""}` });
        lines.push({ kind: "info", text: `count ${result.count ?? es.length}` });
      }
      break;
    }
    case "glob": {
      const ms = result.matches as string[] | undefined;
      if (Array.isArray(ms)) {
        for (const m of ms) lines.push({ kind: "out", text: m });
        lines.push({ kind: "info", text: `count ${result.count ?? ms.length}` });
      }
      break;
    }
    case "memory_query": {
      const ms = result.matches as Array<{ key?: string; content?: string; scope?: string; importance?: number }> | undefined;
      if (Array.isArray(ms)) {
        for (const m of ms) {
          lines.push({ kind: "out", text: `[${m.scope ?? "global"}] ${m.key ?? ""} (imp=${m.importance ?? 0})` });
          if (m.content) lines.push({ kind: "out", text: `  ${m.content}` });
        }
      }
      break;
    }
    case "memory_store":
      lines.push({ kind: "info", text: `stored key=${result.key ?? ""} scope=${result.scope ?? ""}` });
      break;
    case "task_done":
      if (typeof result.summary === "string" && result.summary) lines.push({ kind: "out", text: result.summary });
      break;
    case "get_context_remaining":
      lines.push({
        kind: "info",
        text: `used ${result.used ?? 0} / ${result.window ?? "?"} tokens (remaining ${result.remaining ?? "?"})`,
      });
      break;
    default: {
      // write_file / edit_file / apply_patch / task / 其它
      if (typeof result.stdout === "string" && result.stdout.trim()) lines.push({ kind: "out", text: result.stdout.trim() });
      if (typeof result.stderr === "string" && result.stderr.trim()) lines.push({ kind: "err", text: result.stderr.trim() });
      const scalar = Object.entries(result).filter(
        ([k, v]) => !["stdout", "stderr", "exit", "rejected", "reason", "error"].includes(k) && v !== null && v !== undefined && typeof v !== "object",
      );
      for (const [k, v] of scalar) lines.push(kv(k, v));
    }
  }

  return { name: t.name, state, lines };
}