import React, { useEffect, useMemo, useState } from "react";
import { Box, Text } from "ink";
import path from "node:path";
import { readdir } from "node:fs/promises";
import { theme, charWidth } from "./theme.js";
import { SLASH_COMMANDS } from "./keys.js";

export interface AutocompleteItem {
  display: string;
  insert: string;
  detail?: string;
  kind: "command" | "file";
}

export interface AutocompleteApi {
  get visible(): boolean;
  get items(): AutocompleteItem[];
  get selected(): number;
  move(dir: number): void;
  pick(): void;
}

// 基于 token 的 @ 文件补全（在本地 cwd 下做一层目录匹配）
async function listFileCandidates(token: string): Promise<AutocompleteItem[]> {
  const cwd = process.cwd();
  try {
    const dir = token.includes("/") ? path.dirname(token) : "";
    const base = path.basename(token);
    const fullDir = path.resolve(cwd, dir);
    const entries = await readdir(fullDir, { withFileTypes: true });
    const items: AutocompleteItem[] = [];
    for (const e of entries) {
      const name = e.name;
      if (!name.startsWith(base) || name.startsWith(".")) continue;
      const rel = dir ? `${dir}/${name}` : name;
      items.push({
        display: rel + (e.isDirectory() ? "/" : ""),
        insert: rel + (e.isDirectory() ? "/" : ""),
        detail: e.isDirectory() ? "dir" : "file",
        kind: "file",
      });
    }
    return items.sort((a, b) => a.display.localeCompare(b.display));
  } catch {
    return [];
  }
}

export function isAutocompleteTriggered(value: string, cursor: number): { kind: "command" | "file"; token: string } | null {
  if (value.length === 0) return null;
  const before = value.slice(0, cursor);
  if (before.startsWith("/") && !/\s/.test(before)) {
    return { kind: "command", token: before.slice(1) };
  }
  const at = before.lastIndexOf("@");
  if (at !== -1 && !/\s/.test(before.slice(at + 1))) {
    return { kind: "file", token: before.slice(at + 1) };
  }
  return null;
}

export function Autocomplete({
  value,
  cursor,
  width,
  onSelect,
  register,
}: {
  value: string;
  cursor: number;
  width: number;
  onSelect: (item: AutocompleteItem) => void;
  register: (api: AutocompleteApi | null) => void;
}) {
  const trigger = isAutocompleteTriggered(value, cursor);
  const triggerKey = trigger ? `${trigger.kind}:${trigger.token}` : null;
  const [fileItems, setFileItems] = useState<AutocompleteItem[]>([]);
  const [selected, setSelected] = useState(0);

  const commandItems = useMemo<AutocompleteItem[]>(() => {
    if (trigger?.kind !== "command") return [];
    const token = trigger.token.toLowerCase();
    return SLASH_COMMANDS.filter((c) => c.name.startsWith(token) || c.aliases.some((a) => a.startsWith(token))).map(
      (c) => ({
        display: `/${c.name}`,
        insert: `/${c.name} `,
        detail: c.desc,
        kind: "command" as const,
      }),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [triggerKey]);

  useEffect(() => {
    let live = true;
    setFileItems([]);
    if (trigger?.kind === "file") {
      listFileCandidates(trigger.token).then((items) => {
        if (live) setFileItems(items);
      });
    }
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [triggerKey]);

  useEffect(() => setSelected(0), [triggerKey]);

  const items = trigger?.kind === "command" ? commandItems : fileItems;

  useEffect(() => {
    if (!trigger || items.length === 0) {
      register(null);
      return;
    }
    const shown = items.slice(0, 20);
    const api: AutocompleteApi = {
      get visible() {
        return true;
      },
      get items() {
        return shown;
      },
      get selected() {
        return selected;
      },
      move(dir: number) {
        setSelected((s) => Math.max(0, Math.min(shown.length - 1, s + dir)));
      },
      pick() {
        const item = shown[Math.min(selected, shown.length - 1)];
        if (item) onSelect(item);
      },
    };
    register(api);
    return () => register(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [triggerKey, items, selected]);

  if (!trigger || items.length === 0) {
    if (trigger) {
      return (
        <Box flexDirection="column" borderStyle="single" borderColor={theme.borderMuted}>
          <Box paddingLeft={1} paddingRight={1} backgroundColor={theme.backgroundElement}>
            <Text color={theme.textMuted}>No matching items</Text>
          </Box>
        </Box>
      );
    }
    return null;
  }
  const shown = items.slice(0, 20);
  const sel = Math.min(selected, shown.length - 1);
  return (
    <Box flexDirection="column" borderStyle="single" borderColor={theme.borderMuted}>
      <Box flexDirection="column" backgroundColor={theme.backgroundElement}>
        {shown.map((item, i) => (
          <Box
            key={item.display}
            flexDirection="row"
            paddingLeft={1}
            paddingRight={1}
            backgroundColor={i === sel ? theme.primary : undefined}
          >
            <Box flexShrink={0}>
              <Text color={i === sel ? theme.background : theme.text}>{item.display}</Text>
            </Box>
            {item.detail ? (
              <Text color={i === sel ? theme.background : theme.textMuted} dimColor>
                {"  " + item.detail.slice(0, Math.max(10, width - charWidth(item.display) - 6))}
              </Text>
            ) : null}
          </Box>
        ))}
      </Box>
    </Box>
  );
}
