<script setup lang="ts">
import { ref } from 'vue';
import { useRouter } from 'vue-router';
import { apiPost } from '../api/client';
import { useAuthStore } from '../stores/auth';

const router = useRouter();
const auth = useAuthStore();

const username = ref('');
const password = ref('');
const mode = ref<'login' | 'register'>('login');
const error = ref('');
const loading = ref(false);

async function submit() {
  error.value = '';
  if (!username.value.trim() || !password.value) {
    error.value = '请输入用户名和密码';
    return;
  }
  loading.value = true;
  try {
    const path = mode.value === 'login' ? '/api/auth/login' : '/api/auth/register';
    const data = await apiPost<{ token: string; username: string }>(path, {
      username: username.value.trim(),
      password: password.value,
    });
    auth.setSession(data.token, data.username);
    router.push('/');
  } catch (e) {
    error.value = e instanceof Error ? e.message : '请求失败';
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <h1>chat-forge</h1>
      <p class="subtitle">AI 对话助手 · 全栈参考实现</p>
      <form @submit.prevent="submit">
        <input v-model="username" placeholder="用户名" autocomplete="username" />
        <input v-model="password" type="password" placeholder="密码" autocomplete="current-password" />
        <button type="submit" :disabled="loading">
          {{ loading ? '请稍候…' : mode === 'login' ? '登录' : '注册并登录' }}
        </button>
        <p v-if="error" class="error">{{ error }}</p>
        <p class="switch">
          {{ mode === 'login' ? '还没有账号?' : '已有账号?' }}
          <a @click.prevent="mode = mode === 'login' ? 'register' : 'login'" href="#">
            {{ mode === 'login' ? '去注册' : '去登录' }}
          </a>
        </p>
      </form>
    </div>
  </div>
</template>
