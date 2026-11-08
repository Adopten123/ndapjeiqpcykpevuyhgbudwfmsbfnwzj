import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig(({ mode }) => {
  const target = loadEnv(mode, '.', 'VITE_API_TARGET').VITE_API_TARGET || 'http://127.0.0.1:8090';
  const server = { host: '127.0.0.1', port: 5173, strictPort: true, proxy: { '/api': { target } } };
  return { plugins: [react()], server, preview: server };
});
