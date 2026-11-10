<script setup lang="ts">
import type { Conversation } from '../api/types';

defineProps<{
  conversations: Conversation[];
  currentId: number;
  username: string;
}>();

defineEmits<{
  select: [id: number];
  create: [];
  remove: [id: number];
  logout: [];
}>();
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-header">
      <strong>chat-forge</strong>
      <button class="btn-primary" @click="$emit('create')">+ 新会话</button>
    </div>
    <div class="conv-list">
      <div
        v-for="c in conversations"
        :key="c.id"
        class="conv-item"
        :class="{ active: c.id === currentId }"
        @click="$emit('select', c.id)"
      >
        <span class="conv-title">{{ c.title }}</span>
        <button class="conv-del" @click.stop="$emit('remove', c.id)">×</button>
      </div>
      <p v-if="conversations.length === 0" class="empty-hint">还没有会话,点上方「+ 新会话」开始</p>
    </div>
    <div class="sidebar-footer">
      <span>{{ username }}</span>
      <button class="btn-ghost" @click="$emit('logout')">退出</button>
    </div>
  </aside>
</template>
