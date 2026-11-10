<script setup lang="ts">
import { computed } from 'vue';
import DOMPurify from 'dompurify';
import { marked } from 'marked';
import type { ChatMessage } from '../api/types';

const props = defineProps<{ msg: ChatMessage }>();

// LLM 输出是不可信输入:markdown 渲染前必须过 sanitize,防 XSS
const html = computed(() => {
  const raw = marked.parse(props.msg.content ?? '', { async: false }) as string;
  return DOMPurify.sanitize(raw);
});

const isUser = computed(() => props.msg.role === 'user');
</script>

<template>
  <div class="bubble-row" :class="{ 'bubble-user': isUser }">
    <div class="bubble">
      <div v-if="!isUser && (msg.toolCalls.length > 0 || msg.toolResults.length > 0)" class="tool-zone">
        <details v-for="call in msg.toolCalls" :key="call.id" class="tool-card">
          <summary>🔧 {{ call.name }}</summary>
          <pre>{{ call.arguments }}</pre>
        </details>
        <details v-for="(res, i) in msg.toolResults" :key="'r' + i" class="tool-card tool-result">
          <summary>✅ {{ res.name }} 执行结果</summary>
          <pre>{{ res.result }}</pre>
        </details>
      </div>

      <div v-if="msg.content" class="md-body" v-html="html"></div>

      <div v-if="msg.status === 'streaming' && !msg.content" class="typing">
        <span></span><span></span><span></span>
      </div>

      <div class="bubble-status">
        <span v-if="msg.status === 'streaming' && msg.content" class="status-streaming">生成中…</span>
        <span v-if="msg.status === 'error'" class="status-error">生成失败</span>
        <span v-if="msg.status === 'aborted'" class="status-aborted">已停止生成</span>
      </div>
    </div>
  </div>
</template>
