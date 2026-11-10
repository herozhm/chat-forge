import { useAuthStore } from '../stores/auth';
import type { ChatEvent } from './types';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const auth = useAuthStore();
  const resp = await fetch(path, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      Authorization: auth.token ? 'Bearer ' + auth.token : '',
      ...(init?.headers ?? {}),
    },
  });
  if (resp.status === 401) {
    auth.logout();
    location.href = '/login';
    throw new Error('登录已过期');
  }
  if (!resp.ok) {
    let message = 'HTTP ' + resp.status;
    try {
      message = (await resp.json()).message ?? message;
    } catch {
      // 非 JSON 错误响应
    }
    throw new Error(message);
  }
  return (await resp.json()) as T;
}

export function apiGet<T>(path: string): Promise<T> {
  return request<T>(path);
}

export function apiPost<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, { method: 'POST', body: JSON.stringify(body ?? {}) });
}

export function apiDelete<T>(path: string): Promise<T> {
  return request<T>(path, { method: 'DELETE' });
}

/**
 * 流式对话:fetch + ReadableStream 手解 SSE。
 * 不用 EventSource:它只支持 GET、不能带 Authorization header 和 POST body。
 * 全链路 AbortController:前端 abort 后端 r.Context() 取消,上游 LLM 请求同步取消。
 */
export async function streamChat(args: {
  conversationId: number;
  content: string;
  signal: AbortSignal;
  onEvent: (ev: ChatEvent) => void;
}): Promise<void> {
  const auth = useAuthStore();
  const resp = await fetch('/api/chat', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: 'Bearer ' + auth.token,
    },
    body: JSON.stringify({ conversation_id: args.conversationId, content: args.content }),
    signal: args.signal,
  });
  if (!resp.ok || !resp.body) {
    let message = 'HTTP ' + resp.status;
    try {
      message = (await resp.json()).message ?? message;
    } catch {
      // 非 JSON 错误响应
    }
    throw new Error(message);
  }

  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });

    // SSE 以空行分块;心跳注释行(: ping)无 data 行,过滤后跳过
    let sep = buffer.indexOf('\n\n');
    while (sep >= 0) {
      const block = buffer.slice(0, sep);
      buffer = buffer.slice(sep + 2);

      const lines = block.split('\n');
      const dataLines = lines.filter((l) => l.startsWith('data:'));
      if (dataLines.length === 0) {
        sep = buffer.indexOf('\n\n');
        continue;
      }
      let event = 'message';
      for (const l of lines) {
        if (l.startsWith('event:')) event = l.slice(6).trim();
      }
      const payload = JSON.parse(dataLines.map((l) => l.slice(5).trim()).join('\n'));
      args.onEvent({ event: event as ChatEvent['event'], ...payload });
      sep = buffer.indexOf('\n\n');
    }
  }
}
