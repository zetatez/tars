import type { Message, Session } from "./types.js";

export class API {
  constructor(
    private baseURL: string,
    private key: string,
  ) {}

  private async req<T>(
    method: string,
    path: string,
    body?: unknown,
    idempotencyKey?: string,
  ): Promise<T> {
    const url = this.baseURL.replace(/\/$/, "") + path;
    const headers: Record<string, string> = {
      "Authorization": `Bearer ${this.key}`,
      "Content-Type": "application/json",
    };
    if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
    const resp = await fetch(url, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (resp.status === 204) return undefined as T;
    const text = await resp.text();
    if (!resp.ok) {
      let err = text;
      try {
        err = JSON.parse(text).error ?? text;
      } catch {
        /* keep raw */
      }
      throw new Error(`HTTP ${resp.status}: ${err}`);
    }
    if (!text) return undefined as T;
    return JSON.parse(text) as T;
  }

  health(): Promise<{ status: string }> {
    return this.req("GET", "/healthz");
  }
  version(): Promise<{ name: string; version: string }> {
    return this.req("GET", "/version");
  }

  createSession(cwd?: string, model?: string, promptMode?: string): Promise<Session> {
    return this.req("POST", "/api/v1/session", { cwd, model, prompt_mode: promptMode });
  }
  listSessions(): Promise<{ sessions: Session[] }> {
    return this.req("GET", "/api/v1/session");
  }
  getSession(id: string): Promise<Session> {
    return this.req("GET", `/api/v1/session/${id}`);
  }
  deleteSession(id: string): Promise<void> {
    return this.req("DELETE", `/api/v1/session/${id}`);
  }

  prompt(id: string, text: string, idempotencyKey?: string): Promise<{ turnId: string }> {
    return this.req("POST", `/api/v1/session/${id}/prompt`, { text }, idempotencyKey);
  }
  interrupt(id: string): Promise<void> {
    return this.req("POST", `/api/v1/session/${id}/interrupt`);
  }
  rollback(id: string): Promise<{ rolled_back: boolean }> {
    return this.req("POST", `/api/v1/session/${id}/rollback`);
  }
  async messages(id: string, after = 0, limit = 100): Promise<Message[]> {
    const r = await this.req<{ messages: Message[] }>(
      "GET",
      `/api/v1/session/${id}/messages?after=${after}&limit=${limit}`,
    );
    return r.messages;
  }

  createKey(label?: string): Promise<{ key_id: string; key: string }> {
    return this.req("POST", "/api/v1/keys", { label });
  }
  revokeKey(id: string): Promise<void> {
    return this.req("DELETE", `/api/v1/keys/${id}`);
  }
  getConfig(id: string): Promise<Record<string, unknown>> {
    return this.req("GET", `/api/v1/keys/${id}/config`);
  }
  setConfig(id: string, cfg: Record<string, unknown>): Promise<Record<string, unknown>> {
    return this.req("PUT", `/api/v1/keys/${id}/config`, cfg);
  }
  stats(id: string): Promise<Record<string, number>> {
    return this.req("GET", `/api/v1/keys/${id}/stats`);
  }

  eventURL(id: string, after = 0): string {
    return this.baseURL.replace(/\/$/, "") + `/api/v1/session/${id}/event?after=${after}`;
  }
}
