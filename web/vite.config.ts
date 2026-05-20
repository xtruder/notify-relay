import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  base: '/notify-relay/',
  server: {
    port: 5173,
  },
  build: {
    target: 'es2020',
  },
})
