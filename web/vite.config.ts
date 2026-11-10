import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

// 开发期代理到本地 Go 服务(API_TARGET 可覆盖,默认 8080);生产由 nginx 同源反代
const apiTarget = process.env.API_TARGET ?? 'http://localhost:8080';

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: apiTarget,
        changeOrigin: true,
      },
    },
  },
});
