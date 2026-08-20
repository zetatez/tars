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
  { group: "Session", keys: ["Ctrl+O"], desc: "Expand / collapse long tool output" },
  { group: "Session", keys: ["Ctrl+PageDown / Ctrl+PageUp"], desc: "Page global session list in the top status bar" },
  { group: "Session", keys: ["/"], desc: "Commands: new sessions status models agents init themes skills variants mcps copy export rollback delete editor ssh vim help exit" },
  { group: "Session", keys: ["!"], desc: "Run a shell command on the server via ssh" },
  { group: "Session", keys: ["Ctrl+C"], desc: "Exit (when input empty)" },
];

export const SLASH_COMMANDS: { name: string; aliases: string[]; desc: string }[] = [
  { name: "new", aliases: ["clear"], desc: "Create a new session" },
  { name: "sessions", aliases: ["session", "resume", "continue", "ls"], desc: "Switch to another session" },
  { name: "status", aliases: [], desc: "Show server & session status" },
  { name: "models", aliases: [], desc: "Pick a model" },
  { name: "agents", aliases: [], desc: "Switch agent (Build / Plan)" },
  { name: "init", aliases: [], desc: "Show agent & workspace info" },
  { name: "themes", aliases: [], desc: "Switch theme" },
  { name: "skills", aliases: [], desc: "List skills" },
  { name: "variants", aliases: [], desc: "Pick model variant" },
  { name: "mcps", aliases: [], desc: "List MCP servers" },
  { name: "copy", aliases: [], desc: "Copy session transcript" },
  { name: "export", aliases: [], desc: "Export session transcript to a file" },
  { name: "rollback", aliases: [], desc: "Roll back the most recent system write" },
  { name: "delete", aliases: [], desc: "Delete the current session" },
  { name: "editor", aliases: [], desc: "Compose the prompt in $EDITOR" },
  { name: "ssh", aliases: [], desc: "SSH into the server" },
  { name: "vim", aliases: [], desc: "Edit a remote file (sync back)" },
  { name: "help", aliases: [], desc: "Show help" },
  { name: "exit", aliases: ["quit"], desc: "Exit the application" },
];
