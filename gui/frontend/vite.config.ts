import {defineConfig} from "vite";
import {mkdirSync, writeFileSync} from "node:fs";
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
  plugins: [wails("./bindings"), keepPlaceholder],
});
