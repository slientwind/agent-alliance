import { defineConfig } from "vite";
export default defineConfig({
  server: {
    port: 5180,
    proxy: {
      "/api": { target: "http://127.0.0.1:8180", changeOrigin: false },
      "/healthz": "http://127.0.0.1:8180",
    },
  },
});
