/// <reference types="vite/client" />

declare module "*.po" {
  import type { Messages } from "@lingui/core";
  export const messages: Messages;
}
declare module "moment/locale/zh-cn";
