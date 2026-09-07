import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// The Go binary embeds whatever lands in dist/, so the build output has to
// stay there. In development Vite serves the UI on its own port and proxies
// the API and the event stream to the Go server, which means the frontend
// hot-reloads against real captured mail rather than a mock.
const API_TARGET = process.env.MAILMAN_API ?? 'http://127.0.0.1:8025'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: API_TARGET, changeOrigin: true, ws: true },
      '/healthz': { target: API_TARGET, changeOrigin: true },
    },
  },
})
