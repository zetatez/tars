import type { EventData } from "./types.js";

export type EventHandler = (ev: EventData) => void;

/**
 * 订阅 SSE 事件流：fetch + ReadableStream 解析。
 * 事件帧格式：`id: <seq>\ndata: <json>\n\n` 或 `: heartbeat\n\n`。
 * 返回一个 abort 函数用于中断（Ctrl-C）。
 */
export function streamEvents(
  url: string,
  key: string,
  onEvent: EventHandler,
  onError?: (err: Error) => void,
): () => void {
  const controller = new AbortController();

  (async () => {
    try {
      const resp = await fetch(url, {
        headers: { "Authorization": `Bearer ${key}`, "Accept": "text/event-stream" },
        signal: controller.signal,
      });
      if (!resp.ok || !resp.body) {
        throw new Error(`SSE HTTP ${resp.status}`);
      }

      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buf = "";
      let pendingId: number | undefined;

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });

        // 按空行分帧
        let idx: number;
        while ((idx = buf.indexOf("\n\n")) >= 0) {
          const frame = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          for (const line of frame.split("\n")) {
            if (line.startsWith("id:")) {
              pendingId = parseInt(line.slice(3).trim(), 10) || undefined;
            } else if (line.startsWith("data:")) {
              const payload = line.slice(5).trim();
              if (!payload) continue;
              try {
                const ev = JSON.parse(payload) as EventData;
                if (ev.seq === undefined) ev.seq = pendingId;
                pendingId = undefined;
                onEvent(ev);
              } catch {
                /* 忽略非 JSON 帧 */
              }
            }
          }
        }
      }
    } catch (err) {
      if (controller.signal.aborted) return;
      if (onError) onError(err as Error);
    }
  })();

  return () => controller.abort();
}
