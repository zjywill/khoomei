import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import ui from '@nuxt/ui/vite'

// The build lands in internal/panel/dist and is embedded into the hub binary (docs/PANEL.md#构建).
export default defineConfig({
  base: '/admin/',
  plugins: [
    vue(),
    ui({
      router: false,
      icon: { clientBundle: { scan: true } },
      ui: { colors: { primary: 'amber', neutral: 'stone' } },
    }),
  ],
  build: { outDir: '../internal/panel/dist', emptyOutDir: true, assetsDir: 'assets', chunkSizeWarningLimit: 1500 },
  // Loopback is the hub owner, so a local hub needs no admins entry during development. Host is kept
  // as localhost:5173 so that it matches the Origin the prune check compares it with.
  server: { proxy: { '/admin/api': { target: 'http://127.0.0.1:7767', changeOrigin: false } } },
})
