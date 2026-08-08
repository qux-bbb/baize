import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': 'http://localhost:8080'
    }
  },
  build: {
    outDir: '../cmd/web',
    // outDir 在项目根之外时 vite 默认不清空，显式开启避免旧 hash 文件堆积（embed 膨胀）
    emptyOutDir: true,
    sourcemap: false
  }
})
