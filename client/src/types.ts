export interface Session {
  id: string;
  key_id?: string;
  cwd: string;
  status: string;
  model: string;
  provider?: string;
}

export interface ToolCall {
  id: string;
  name: string;
  args: unknown;
  result?: unknown;
  status?: string;
}

export interface MessageContent {
  v?: number;
  text?: string;
  files?: string[];
  tools?: ToolCall[];
  error?: string;
}

export interface Message {
  id: string;
  seq: number;
  role: string;
  content: MessageContent;
}

export interface EventData {
  type: string;
  seq?: number;
  data?: unknown;
}

export interface ApprovalRequest {
  id: string;
  action: string;
  resource: string;
  status: string;
  created?: number;
}
