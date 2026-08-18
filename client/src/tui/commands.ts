import { sshInteractive, sshRun, sshReadFile, sshWriteFile, type SshTarget } from "./ssh.js";
import { openEditor } from "./editor.js";

type Toast = (text: string, kind?: "info" | "error" | "warning") => void;

// 交互式 ssh 登录
export function runSsh(target: SshTarget, showToast: Toast): void {
  void sshInteractive(target).then((code) => {
    if (code !== 0) showToast(`ssh 退出 (code ${code})`, code === 0 ? "info" : "error");
  });
}

// 服务端执行 shell 命令（! 命令）
export function runBang(target: SshTarget, cmd: string, showToast: Toast): void {
  void sshRun(target, cmd).then((code) => {
    showToast(code === 0 ? `完成 (exit 0)` : `失败 (exit ${code})`, code === 0 ? "info" : "error");
  });
}

// /vim <path>：远程读取 → 本地编辑 → 同步回写（尽量保留权限属组）
export async function runVim(target: SshTarget, path: string, showToast: Toast): Promise<void> {
  if (!path) {
    showToast("用法：/vim <file-path>", "warning");
    return;
  }
  let content: string;
  try {
    content = await sshReadFile(target, path);
  } catch (err) {
    showToast(`读取远程文件失败：${(err as Error).message}`, "error");
    return;
  }
  showToast(`正在编辑 ${path}（本地 ${process.env.EDITOR || process.env.VISUAL || "vi"}）`);
  const edited = await openEditor(content);
  if (edited === null) {
    showToast("已取消（无改动同步）", "info");
    return;
  }
  try {
    const r = await sshWriteFile(target, path, edited);
    showToast(
      `已同步 ${path}（mode ${r.mode}${r.chownOk ? "" : "，属主变更未保留"}）`,
      r.chownOk ? "info" : "warning",
    );
  } catch (err) {
    showToast(`写回失败：${(err as Error).message}`, "error");
  }
}
