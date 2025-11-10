import { defineConfig } from "astro/config";
import node from "@astrojs/node";
import tailwind from "@astrojs/tailwind";

export default defineConfig({
  integrations: [tailwind({ applyBaseStyles: false })],
  adapter: node({ mode: "standalone" }),
  output: "server",
  server: {
    port: Number(process.env.PORT || 4321),
  },
});
