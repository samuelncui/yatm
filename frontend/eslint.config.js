import eslint from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import unusedImports from "eslint-plugin-unused-imports";
import tseslint from "typescript-eslint";

const feedbackRules = [
  {
    selector: "JSXOpeningElement[name.name=/^(div|span|p)$/]:has(JSXAttribute[name.name='role'] Literal[value='alert'])",
    message: "Inline error regions use Feedback and its action slot instead of assembling an alert from native elements.",
  },
  {
    selector: "JSXOpeningElement:matches([name.name='Alert'], [name.property.name='Alert']):has(JSXAttribute[name.name='severity'] Literal[value='error'])",
    message: "Inline errors use Feedback, with recovery controls in its action slot, so layout and spacing have one owner.",
  },
  {
    selector: "JSXOpeningElement:matches([name.name='Alert'], [name.property.name='Alert']):has(JSXAttribute[name.name='action'])",
    message: "Alerts with actions use Feedback and its action slot for consistent spacing and alignment.",
  },
  {
    selector:
      "JSXElement:has(> JSXOpeningElement:matches([name.name='Alert'], [name.property.name='Alert'])):has(JSXOpeningElement:matches([name.name='Button'], [name.property.name='Button']))",
    message: "Put Alert controls in Feedback's action slot instead of the message body.",
  },
];

// Each shared read has one owner. A new caller reuses that owner instead of
// introducing a parallel cache, loading state or pagination implementation.
const readOwners = [
  {
    file: "src/components/job-results-dialog.tsx",
    selector:
      "MemberExpression:matches([object.name='scanJobCli'][property.name='listEntries'], [object.name='archiveJobCli'][property.name='listFiles'], [object.name='restoreJobCli'][property.name='listFiles'])",
    message: "Job item listings page through JobResultsDialog; add a view instead of reading the listing here.",
  },
  {
    file: "src/state/locations.ts",
    selector: "MemberExpression[object.name='locationCli'][property.name='list']",
    message: "Configured Location choices use useLocationChoices and the shared Locations catalog.",
  },
  {
    file: "src/components/settings-editor.ts",
    selector: "MemberExpression[object.name='settingsCli'][property.name=/^(get|update)$/]",
    message: "Committed Settings use the shared Settings editor/cache so reads, saves and stale responses have one owner.",
  },
];
const syntaxRules = (owner) => [
  "error",
  ...(owner === "src/components/feedback.tsx" ? [] : feedbackRules),
  ...readOwners.filter(({ file }) => file !== owner).map(({ selector, message }) => ({ selector, message })),
];

export default [
  {
    ignores: ["dist/**/*", "node_modules/**/*", "src/entity/*.go", "src/entity/*.ts", "src/api/*.ts"],
  },
  eslint.configs.recommended,
  ...tseslint.configs.recommended,
  reactHooks.configs.flat.recommended,
  {
    files: ["src/**/*.{ts,tsx}"],
    plugins: {
      "unused-imports": unusedImports,
    },
    rules: {
      "no-alert": "error",
      "no-restricted-syntax": syntaxRules(),
      "@typescript-eslint/no-explicit-any": "off",
      "react-hooks/set-state-in-effect": "off",
      "no-unused-vars": "off",
      "@typescript-eslint/no-unused-vars": "off",
      "no-undef": "off",
      "unused-imports/no-unused-imports": "error",
      "unused-imports/no-unused-vars": [
        "warn",
        {
          vars: "all",
          varsIgnorePattern: "^_",
          args: "after-used",
          argsIgnorePattern: "^_",
        },
      ],
    },
  },
  ...[...readOwners.map(({ file }) => file), "src/components/feedback.tsx"].map((file) => ({
    files: [file],
    rules: { "no-restricted-syntax": syntaxRules(file) },
  })),
];
