import { i18n } from "@lingui/core";
import { compileMessage } from "@lingui/message-utils/compileMessage";

// Tests render the English source text without loading a catalog.
i18n.setMessagesCompiler(compileMessage);
i18n.loadAndActivate({ locale: "en", messages: {} });

if (typeof window !== "undefined") {
  await import("./setup-dom");
}

export {};
