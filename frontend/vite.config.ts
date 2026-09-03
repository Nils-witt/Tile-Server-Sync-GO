import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// Dev-server-only proxy: `npm run dev` serves the SPA on Vite's own port
// while forwarding every backend-owned path to the real Go server (started
// separately, e.g. `go run ./cmd/Tile-Server-Sync-GO -config config.yaml`).
// The production build has no proxy — the Go binary embeds and serves
// frontend/dist itself (see internal/webserver/spa.go).
const backend = process.env.VITE_BACKEND ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': backend,
      '/login/sso': backend,
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
