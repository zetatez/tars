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

      const emitFrame = (frame: string) => {
        let data = "";
        for (const line of frame.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n")) {
          if (line.startsWith("id:")) {
            pendingId = parseInt(line.slice(3).trim(), 10) || undefined;
          } else if (line.startsWith("data:")) {
            data += (data ? "\n" : "") + line.slice(5).replace(/^ /, "");
          }
        }
        if (!data) return;
        try {
          const ev = JSON.parse(data) as EventData;
          if (ev.seq === undefined) ev.seq = pendingId;
          pendingId = undefined;
          onEvent(ev);
        } catch (err) {
          onError?.(new Error(`invalid SSE data: ${(err as Error).message}`));
        }
      };

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });

        // 按空行分帧，兼容 LF/CRLF。
        let idx: number;
        while ((idx = buf.search(/\r?\n\r?\n/)) >= 0) {
          const frame = buf.slice(0, idx);
          const separator = buf.match(/\r?\n\r?\n/)?.[0].length ?? 2;
          buf = buf.slice(idx + separator);
          emitFrame(frame);
        }
      }
      const tail = buf.trim();
      if (tail) emitFrame(tail);
    } catch (err) {
      if (controller.signal.aborted) return;
      if (onError) onError(err as Error);
    }
  })();

  return () => controller.abort();
}
