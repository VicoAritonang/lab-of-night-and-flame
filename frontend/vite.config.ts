import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { renderMetadata } from "./src/site";

export default defineConfig({
  plugins: [react(), {
    name: "site-metadata",
    transformIndexHtml: (html, context) => {
      const path = (context.originalUrl ?? context.path).split("?")[0];
      return html.replace(/<!-- site:head:start -->[\s\S]*?<!-- site:head:end -->/, `<!-- site:head:start -->\n${renderMetadata(path === "/index.html" ? "/" : path)}\n<!-- site:head:end -->`);
    },
  }],
  server: {
    port: 5173,
    proxy: { "/api": { target: "http://127.0.0.1:8000", changeOrigin: true, ws: true } },
  },
  build: {
    target: "es2021",
    cssCodeSplit: true,
    sourcemap: false,
    rollupOptions: { output: { manualChunks: { react: ["react", "react-dom"] } } },
  },
});
