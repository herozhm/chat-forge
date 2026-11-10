import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

// 开发期代理到本地 Go 服务;生产由 nginx 同源反代(见 server Dockerfile/部署文档)
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
});
