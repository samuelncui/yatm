import assert from "node:assert/strict";
import test from "node:test";
import { styleViolations } from "./check-styles.mjs";

test("catches the historical overrides without rejecting layout or supported slots", () => {
  assert.equal(styleViolations("page.less", ".app-MuiButton-root { padding: 8px }").length, 1);
  assert.equal(styleViolations("page.tsx", 'sx={{ "& .app-MuiDialog-paper": {} }}').length, 1);
  assert.equal(styleViolations("page.less", ".chonky-baseButton { &:hover { color: blue !important; } }").length, 1);
  assert.equal(styleViolations("page.less", ".chonky-baseButton { margin: 4px; }").length, 1);
  assert.equal(styleViolations("page.less", ".chonky-chonkyRoot { border-radius: 12px !important; }").length, 1);
  assert.equal(styleViolations("page.less", ".selection-browser > .file-browser { flex: 1; }").length, 0);
  assert.equal(styleViolations("page.tsx", "sx={{ [`& .${dialogClasses.paper}`]: { margin: 1 } }}").length, 0);
  assert.equal(styleViolations("mui-classnames.test.tsx", 'expect(buttonClasses.root).toBe("app-MuiButton-root")').length, 0);
});
