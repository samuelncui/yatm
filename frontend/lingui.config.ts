import { defineConfig } from "@lingui/cli";
import { formatter } from "@lingui/format-po";

export default defineConfig({
  sourceLocale: "en",
  locales: ["en", "zh-CN"],
  catalogs: [{ path: "<rootDir>/src/locales/{locale}/messages", include: ["src"], exclude: ["**/*.test.*", "src/entity/**"] }],
  // Source positions change with every edit; leaving them out keeps catalog diffs to message changes.
  format: formatter({ origins: false, lineNumbers: false }),
});
