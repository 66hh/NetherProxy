import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { viteSingleFile } from 'vite-plugin-singlefile'

export default defineConfig({
  plugins: [react(), viteSingleFile()],
  build: {
    outDir: '../internal/server/gateway/web',
    emptyOutDir: true,
  },
  server: {
    proxy: { '/api': 'http://127.0.0.1:19130' },
  },
})
