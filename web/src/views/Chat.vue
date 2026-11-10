<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { apiDelete, apiGet, apiPost, streamChat } from '../api/client';
import { useAuthStore } from '../stores/auth';
import type { ChatEvent, ChatMessage, Conversation, ToolCall } from '../api/types';
import MessageBubble from '../components/MessageBubble.vue';
import ConversationSidebar from '../components/ConversationSidebar.vue';

const auth = useAuthStore();

const conversations = ref<Conversation[]>([]);
const currentId = ref(0);
const messages = ref<ChatMessage[]>([]);
const input = ref('');
const streaming = ref(false);
let controller: AbortController | null = null;

const offline = ref(!navigator.onLine);
const errorMsg = ref('');

const listEl = ref<HTMLElement | null>(null);

onMounted(() => {
  void loadConversations();
  window.addEventListener('online', updateOnline);
  window.addEventListener('offline', updateOnline);
});

onBeforeUnmount(() => {
  window.removeEventListener('online', updateOnline);
  controller?.abort();
});

function updateOnline(): void {
  offline.value = !navigator.onLine;
}

async function loadConversations(): Promise<void> {
  conversations.value = await apiGet<Conversation[]>('/api/conversations');
  if (conversations.value.length > 0 && currentId.value === 0) {
    await selectConversation(conversations.value[0].id);
  }
}

async function newConversation(): Promise<void> {
  if (streaming.value) return;
  const conv = await apiPost<Conversation>('/api/conversations', { title: '新会话' });
  conversations.value.unshift(conv);
  await selectConversation(conv.id);
}

async function selectConversation(id: number): Promise<void> {
  if (streaming.value) stop();
  currentId.value = id;
  const raw = await apiGet<Array<{ id: number; role: string; content: string; tool_calls_json?: string }>>(
    '/api/conversations/' + id + '/messages',
  );
  messages.value = raw.map((m) => ({
    key: String(m.id),
    role: m.role as ChatMessage['role'],
    content: m.content,
    status: 'done',
    toolCalls: parseToolCalls(m.tool_calls_json),
    toolResults: [],
  }));
  scrollToBottom();
}

function parseToolCalls(json: string | undefined | null): ToolCall[] {
  if (!json) return [];
  try {
    return JSON.parse(json) as ToolCall[];
  } catch {
    return [];
  }
}

function removeConversation(id: number): void {
  if (streaming.value) return;
  if (!confirm('删除该会话及全部消息?')) return;
  apiDelete('/api/conversations/' + id).then(() => {
    conversations.value = conversations.value.filter((c) => c.id !== id);
    if (currentId.value === id) {
      currentId.value = 0;
      messages.value = [];
      if (conversations.value.length > 0) {
        void selectConversation(conversations.value[0].id);
      }
    }
  });
}

function handleEvent(a: ChatMessage, ev: ChatEvent): void {
  switch (ev.event) {
    case 'delta':
      a.content += ev.content ?? '';
      scrollToBottom();
      break;
    case 'tool_call':
      if (ev.tool_call) a.toolCalls.push(ev.tool_call);
      scrollToBottom();
      break;
    case 'tool_result':
      if (ev.tool_result) a.toolResults.push(ev.tool_result);
      scrollToBottom();
      break;
    case 'done':
      a.status = 'done';
      break;
    case 'error':
      a.status = 'error';
      errorMsg.value = ev.message ?? '生成失败';
      break;
  }
}

async function send(): Promise<void> {
  const content = input.value.trim();
  if (!content || streaming.value || currentId.value === 0) return;
  errorMsg.value = '';
  input.value = '';
  streaming.value = true;

  messages.value.push({ key: 'u' + Date.now(), role: 'user', content, status: 'done', toolCalls: [], toolResults: [] });
  const a: ChatMessage = { key: 'a' + Date.now(), role: 'assistant', content: '', status: 'streaming', toolCalls: [], toolResults: [] };
  messages.value.push(a);
  scrollToBottom();

  controller = new AbortController();
  try {
    await streamChat({
      conversationId: currentId.value,
      content,
      signal: controller.signal,
      onEvent: (ev) => handleEvent(a, ev),
    });
    if (a.status === 'streaming') a.status = 'done';
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') {
      a.status = 'aborted';
    } else {
      a.status = 'error';
      errorMsg.value = e instanceof Error ? e.message : '请求失败';
    }
  } finally {
    streaming.value = false;
    controller = null;
    void loadConversations();
  }
}

function stop(): void {
  controller?.abort();
  controller = null;
  streaming.value = false;
}

function onKeydown(e: KeyboardEvent): void {
  if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
    e.preventDefault();
    void send();
  }
}

watch(
  () => messages.value.length,
  () => scrollToBottom(),
);

function scrollToBottom(): void {
  void nextTick(() => {
    const el = listEl.value;
    if (el) el.scrollTop = el.scrollHeight;
  });
}
</script>

<template>
  <div class="chat-page">
    <ConversationSidebar
      :conversations="conversations"
      :current-id="currentId"
      :username="auth.username"
      @select="selectConversation"
      @create="newConversation"
      @remove="removeConversation"
      @logout="auth.logout(); $router.push('/login')"
    />

    <main class="chat-main">
      <div v-if="offline" class="banner banner-offline">网络已断开,恢复后自动可继续</div>
      <div v-if="errorMsg" class="banner banner-error">{{ errorMsg }}</div>

      <div ref="listEl" class="message-list">
        <div v-if="currentId === 0" class="empty-state">
          <h2>chat-forge</h2>
          <p>新建一个会话,开始与 AI 对话。</p>
          <p class="hint">默认运行离线 Mock 模型,零配置即可体验完整流式与工具调用流程。</p>
        </div>
        <MessageBubble v-for="m in messages" :key="m.key" :msg="m" />
      </div>

      <div class="composer">
        <textarea
          v-model="input"
          :disabled="streaming"
          placeholder="输入消息,Ctrl/⌘+Enter 发送;试试发送「今天杭州天气怎么样」演示工具调用"
          rows="3"
          @keydown="onKeydown"
        ></textarea>
        <div class="composer-actions">
          <span class="status-hint" v-if="streaming">生成中…</span>
          <button v-if="streaming" class="btn-ghost" @click="stop">停止生成</button>
          <button v-else class="btn-primary" @click="send" :disabled="!input.trim() || currentId === 0">发送</button>
        </div>
      </div>
    </main>
  </div>
</template>
