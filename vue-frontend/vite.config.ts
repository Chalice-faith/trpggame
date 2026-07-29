import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import path from 'path'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src')
    }
  },
  test: {
    clearMocks: true,
    restoreMocks: true
  },
  server: {
    port: 5173,
    host: '0.0.0.0'
  }
})
