import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The API base is injected at build time (VITE_API_BASE). Locally it defaults to
// the Go server on :8080; on Vercel it points at the deployed API. Nothing about
// the frontend is environment-specific beyond this one value.
export default defineConfig({
  plugins: [react()],
  server: {
    // 5173 is already in the API's allowedOrigins, so CORS works with no changes.
    port: 5173,
  },
});
