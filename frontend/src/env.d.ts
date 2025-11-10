/// <reference path="../.astro/types.d.ts" />
/// <reference types="astro/client" />

interface ImportMetaEnv {
  readonly PUBLIC_API_BASE?: string;
  readonly PUBLIC_WS_BASE?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
