import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": { target: "http://127.0.0.1:8000", changeOrigin: true } },
  },
  build: {
    target: "es2021",
    cssCodeSplit: true,
    sourcemap: false,
    rollupOptions: { output: { manualChunks: { react: ["react", "react-dom"] } } },
  },
});
