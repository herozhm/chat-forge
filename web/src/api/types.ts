// 与后端契约对齐的类型定义

export interface Conversation {
  id: number;
  user_id: number;
  title: string;
  created_at: string;
  updated_at: string;
}

export interface ToolCall {
  id: string;
  name: string;
  arguments: string;
}

export interface ToolResult {
  call_id: string;
  name: string;
  result: string;
}

export interface ChatEvent {
  event: 'delta' | 'tool_call' | 'tool_result' | 'done' | 'error';
  content?: string;
  tool_call?: ToolCall;
  tool_result?: ToolResult;
  message?: string;
}

// 气泡状态机:idle 无意义,streaming 接收中,error/aborted 为显式终态——
// 任何中断都必须收敛到终态,不允许永久 loading。
export type MessageStatus = 'done' | 'streaming' | 'error' | 'aborted';

export interface ChatMessage {
  key: string;
  role: 'user' | 'assistant' | 'tool';
  content: string;
  status: MessageStatus;
  toolCalls: ToolCall[];
  toolResults: ToolResult[];
}
