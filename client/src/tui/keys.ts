// 键位说明（用于帮助对话框；与 opencode 默认键位对齐的核心子集）
export interface KeyDesc {
  keys: string[];
  desc: string;
  group: string;
}

export const KEY_HELP: KeyDesc[] = [
  { group: "Prompt", keys: ["Enter"], desc: "Submit" },
  { group: "Prompt", keys: ["Shift+Enter", "Ctrl+J", "Alt+Enter"], desc: "Insert newline" },
  { group: "Prompt", keys: ["↑ / ↓"], desc: "Navigate prompt history (at line start/end)" },
  { group: "Prompt", keys: ["Ctrl+A / Ctrl+E", "Home / End"], desc: "Start / end of line" },
  { group: "Prompt", keys: ["Ctrl+B / Ctrl+F", "← / →"], desc: "Move cursor left / right" },
  { group: "Prompt", keys: ["Alt+B / Alt+F", "Ctrl+← / Ctrl+→"], desc: "Move word left / right" },
  { group: "Prompt", keys: ["Ctrl+K"], desc: "Delete to end of line" },
  { group: "Prompt", keys: ["Ctrl+U"], desc: "Delete to start of line" },
  { group: "Prompt", keys: ["Ctrl+W", "Alt+Backspace"], desc: "Delete word backward" },
  { group: "Prompt", keys: ["Alt+D", "Alt+Delete"], desc: "Delete word forward" },
  { group: "Prompt", keys: ["Ctrl+Shift+D"], desc: "Delete line" },
  { group: "Prompt", keys: ["Ctrl+- / Ctrl+."], desc: "Undo / Redo" },
  { group: "Prompt", keys: ["Ctrl+C"], desc: "Clear input (empty: exit)" },
  { group: "Prompt", keys: ["Ctrl+V"], desc: "Paste from clipboard" },
  { group: "Prompt", keys: ["/"], desc: "Slash commands" },
  { group: "Prompt", keys: ["@"], desc: "Mention files" },
  { group: "Session", keys: ["PageUp / PageDown"], desc: "Scroll messages by page" },
  { group: "Session", keys: ["Tab"], desc: "Switch build / plan mode" },
  { group: "Session", keys: ["Esc"], desc: "Interrupt (press twice to confirm)" },
  { group: "Session", keys: ["Ctrl+X"], desc: "Leader: n=new, l=session list, e=editor, q=exit, y=copy, h=help" },
  { group: "Session", keys: ["/"], desc: "Slash commands: /new /resume /copy /export /rollback /delete /editor /help /exit" },
  { group: "Session", keys: ["Ctrl+C"], desc: "Exit (when input empty)" },
];

export const SLASH_COMMANDS: { name: string; aliases: string[]; desc: string }[] = [
  { name: "new", aliases: ["clear"], desc: "Create a new session" },
  { name: "resume", aliases: ["sessions", "continue"], desc: "Switch to another session" },
  { name: "copy", aliases: [], desc: "Copy session transcript" },
  { name: "export", aliases: [], desc: "Export session transcript to a file" },
  { name: "rollback", aliases: [], desc: "Roll back the most recent system write" },
  { name: "delete", aliases: [], desc: "Delete the current session" },
  { name: "editor", aliases: [], desc: "Compose the prompt in $EDITOR" },
  { name: "help", aliases: [], desc: "Show help" },
  { name: "exit", aliases: ["quit"], desc: "Exit the application" },
];
