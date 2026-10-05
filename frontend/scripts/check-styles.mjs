import console from "node:console";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

export function styleViolations(path, source) {
  const failures = [];
  if (path !== "mui-classnames.test.tsx" && /app-Mui[A-Za-z]/.test(source)) {
    failures.push("Use exported MUI utility classes or component slots, not generated app-Mui class names.");
  }
  if (/\.(?:css|less)$/.test(path) && /\.chonky-[A-Za-z]/.test(source)) {
    failures.push("Use the application FileBrowser and Chonky theme props, not stylesheet selectors into Chonky's DOM.");
  }
  return failures;
}

export function checkStyles(root) {
  const errors = [];
  for (const entry of readdirSync(root, { withFileTypes: true, recursive: true })) {
    if (!entry.isFile() || !/\.(?:css|less|tsx?)$/.test(entry.name)) continue;
    const path = join(entry.parentPath, entry.name);
    const name = relative(root, path).replaceAll("\\", "/");
    if (name.startsWith("entity/") || name.startsWith("api/")) continue;
    for (const message of styleViolations(name, readFileSync(path, "utf8"))) errors.push(`${name}: ${message}`);
  }
  return errors;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const errors = checkStyles(resolve(dirname(fileURLToPath(import.meta.url)), "../src"));
  if (errors.length) {
    console.error(errors.join("\n"));
    process.exitCode = 1;
  } else console.log("Shared style boundaries passed.");
}
