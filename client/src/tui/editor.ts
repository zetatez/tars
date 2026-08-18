import { spawn } from "node:child_process";
import { mkdtemp, writeFile, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

// 用 $EDITOR 编辑临时文件，返回编辑后的内容；取消返回 null
export async function openEditor(initial: string): Promise<string | null> {
  try {
    const dir = await mkdtemp(path.join(tmpdir(), "tars-edit-"));
    const file = path.join(dir, "prompt.md");
    await writeFile(file, initial);
    const editor = process.env.EDITOR || process.env.VISUAL || "vi";
    const wasRaw = process.stdin.isRaw;
    process.stdin.setRawMode(false);
    return await new Promise<string | null>((resolve) => {
      const child = spawn(editor, [file], { stdio: "inherit" });
      child.on("error", () => {
        process.stdin.setRawMode(!!wasRaw);
        resolve(null);
      });
      child.on("exit", async (code) => {
        process.stdin.setRawMode(!!wasRaw);
        if (code !== 0) return resolve(null);
        try {
          const content = await readFile(file, "utf8");
          resolve(content.trim());
        } catch {
          resolve(null);
        }
      });
    });
  } catch {
    return null;
  }
}
