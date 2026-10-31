import {defineConfig} from "vite";
import {mkdirSync, writeFileSync} from "node:fs";
import {fileURLToPath} from "node:url";
import wails from "@wailsio/runtime/plugins/vite";

// Vite empties dist/ before each build. The Go side embeds that folder, and
// the repository tracks an empty placeholder in it so that "go build" works
// without an npm build; put the placeholder back after the build.
const keepPlaceholder = {
  name: "keep-dist-placeholder",
  closeBundle() {
    mkdirSync("dist", {recursive: true});
    writeFileSync("dist/.gitkeep", "");
  },
};

// https://vitejs.dev/config/
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  build: {
    // Two pages: the main window and the small notification window (macOS).
    rollupOptions: {
      input: {
        main: fileURLToPath(new URL("./index.html", import.meta.url)),
        toast: fileURLToPath(new URL("./toast.html", import.meta.url)),
      },
    },
  },
  plugins: [wails("./bindings"), keepPlaceholder],
});
