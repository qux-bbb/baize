import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      // 后端是 TLS 服务（自签证书），dev 代理必须用 https + secure:false
      '/api': {
        target: 'https://localhost:8080',
        changeOrigin: true,
        secure: false,
      }
    }
  },
  build: {
    outDir: '../cmd/web',
    // outDir 在项目根之外时 vite 默认不清空，显式开启避免旧 hash 文件堆积（embed 膨胀）
    emptyOutDir: true,
    sourcemap: false
  }
})
