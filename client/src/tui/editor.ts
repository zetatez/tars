import { spawn } from "node:child_process";
import { writeFile, readFile, unlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { withSuspended } from "./suspend.js";

function pickEditor(): string {
  return process.env.EDITOR || process.env.VISUAL || "vi";
}

// 用 $EDITOR（默认 vi/vim）在 /tmp 编辑临时文件，返回编辑后的内容；取消返回 null。
// 保存退出后临时文件会被删除。
export async function openEditor(initial: string): Promise<string | null> {
  const file = path.join(tmpdir(), `tars-edit-${process.pid}-${Date.now()}.md`);
  try {
    await writeFile(file, initial);
  } catch {
    return null;
  }
  const cleanup = async () => {
    try {
      await unlink(file);
    } catch {
      // 忽略清理失败
    }
  };
  const run = async (): Promise<string | null> => {
    const editor = pickEditor();
    return await new Promise<string | null>((resolve) => {
      const child = spawn(editor, [file], { stdio: "inherit" });
      const done = (result: string | null) => {
        void cleanup();
        resolve(result);
      };
      child.on("error", () => done(null));
      child.on("exit", async (code) => {
        if (code !== 0) return done(null);
        try {
          const content = await readFile(file, "utf8");
          done(content.trim());
        } catch {
          done(null);
        }
      });
    });
  };
  try {
    return await withSuspended(run);
  } catch {
    void cleanup();
    return null;
  }
}