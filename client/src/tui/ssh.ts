import { execFile, spawn } from "node:child_process";
import { withSuspended } from "./suspend.js";

export interface SshTarget {
  host: string;
  user: string;
  port: number;
}

// 从 base-url 推断 ssh 目标主机（tars server 与 ssh 目标通常同机）
export function sshTargetFromBase(baseUrl: string, user?: string, port?: number): SshTarget {
  let host = "127.0.0.1";
  try {
    host = new URL(baseUrl).hostname || host;
  } catch {
    /* ignore */
  }
  return { host, user: user || process.env.USER || "root", port: port && port > 0 ? port : 22 };
}

function dest(t: SshTarget): string {
  return t.user ? `${t.user}@${t.host}` : t.host;
}

function nonInteractiveArgs(t: SshTarget, cmd: string): string[] {
  const a = ["-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=10"];
  if (t.port !== 22) a.push("-p", String(t.port));
  a.push(dest(t), cmd);
  return a;
}

function interactiveArgs(t: SshTarget): string[] {
  const a = ["-o", "StrictHostKeyChecking=accept-new"];
  if (t.port !== 22) a.push("-p", String(t.port));
  a.push(dest(t));
  return a;
}

function shq(s: string): string {
  return "'" + s.replace(/'/g, `'\\''`) + "'";
}

export interface SshResult {
  code: number;
  stdout: string;
  stderr: string;
}

// 非交互执行远程命令（! 命令 / 文件操作）
export function sshExec(t: SshTarget, cmd: string): Promise<SshResult> {
  return new Promise((resolve) => {
    execFile("ssh", nonInteractiveArgs(t, cmd), { maxBuffer: 16 * 1024 * 1024 }, (err, stdout, stderr) => {
      const code = err ? (typeof (err as { code?: unknown }).code === "number" ? ((err as { code?: number }).code as number) : 1) : 0;
      resolve({ code, stdout, stderr });
    });
  });
}

// 交互式 ssh 登录（/ssh），输出直接进终端（可交互输入密码）
export function sshInteractive(t: SshTarget): Promise<number> {
  return withSuspended(
    () =>
      new Promise<number>((resolve) => {
        const child = spawn("ssh", interactiveArgs(t), { stdio: "inherit" });
        child.on("error", () => resolve(255));
        child.on("exit", (code) => resolve(code ?? 0));
      }),
  );
}

// 服务端执行命令，输出流式进终端（! 命令）
export function sshRun(t: SshTarget, cmd: string): Promise<number> {
  return withSuspended(
    () =>
      new Promise<number>((resolve) => {
        const child = spawn("ssh", nonInteractiveArgs(t, cmd), { stdio: "inherit" });
        child.on("error", () => resolve(255));
        child.on("exit", (code) => resolve(code ?? 0));
      }),
  );
}

// 读取远程文件内容
export async function sshReadFile(t: SshTarget, path: string): Promise<string> {
  const r = await sshExec(t, `cat ${shq(path)}`);
  if (r.code !== 0) throw new Error(r.stderr.trim() || `cat failed (${r.code})`);
  return r.stdout;
}

export interface FileMeta {
  uid: string;
  gid: string;
  mode: string;
}

// 读取远程文件 owner/group/权限
export async function sshFileMeta(t: SshTarget, path: string): Promise<FileMeta> {
  const r = await sshExec(t, `stat -c '%u %g %a' ${shq(path)}`);
  if (r.code !== 0) throw new Error(r.stderr.trim() || `stat failed (${r.code})`);
  const [uid, gid, mode] = r.stdout.trim().split(/\s+/);
  return { uid: uid || "", gid: gid || "", mode: mode || "644" };
}

// 写回远程文件，尽量保持权限/属组（chown 需要 root 或同属主，失败则提示）
export async function sshWriteFile(t: SshTarget, path: string, content: string): Promise<{ mode: string; chownOk: boolean }> {
  const meta = await sshFileMeta(t, path);
  const tmp = `/tmp/.tars-vim-${process.pid}-${Date.now()}`;
  // 1) 写入临时文件（stdin 管道）
  await new Promise<void>((resolve, reject) => {
    const child = spawn("ssh", nonInteractiveArgs(t, `cat > ${shq(tmp)}`), { stdio: ["pipe", "ignore", "inherit"] });
    child.stdin.on("error", () => {});
    child.stdin.write(content);
    child.stdin.end();
    child.on("error", reject);
    child.on("exit", (code) => (code === 0 ? resolve() : reject(new Error(`ssh write failed (${code})`))));
  });
  // 2) 覆盖目标（cp -p 保留权限；属主尽力保留）
  const copy = await sshExec(t, `cp -p ${shq(tmp)} ${shq(path)}`);
  // 3) 恢复权限
  await sshExec(t, `chmod ${meta.mode} ${shq(path)}`);
  // 4) 尽力恢复属主
  let chownOk = true;
  if (meta.uid && meta.gid) {
    const ch = await sshExec(t, `chown ${meta.uid}:${meta.gid} ${shq(path)}`);
    chownOk = ch.code === 0;
  }
  await sshExec(t, `rm -f ${shq(tmp)}`);
  if (copy.code !== 0) throw new Error(copy.stderr.trim() || `copy failed (${copy.code})`);
  return { mode: meta.mode, chownOk };
}
