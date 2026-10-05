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
      "no-restricted-syntax": ["error", ...feedbackRules],
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
  {
    // Every Job item list pages through the shared results module, which owns the
    // window cache, the result-set total and the anchor/continuation requests.
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["src/components/job-results-dialog.tsx"],
    rules: {
      "no-restricted-syntax": [
        "error",
        ...feedbackRules,
        {
          selector: "MemberExpression[object.name='scanJobCli'][property.name='listEntries']",
          message: "Job item listings page through JobResultsDialog; add a view instead of reading the listing here.",
        },
        {
          selector: "MemberExpression[object.name='archiveJobCli'][property.name='listFiles']",
          message: "Job item listings page through JobResultsDialog; add a view instead of reading the listing here.",
        },
        {
          selector: "MemberExpression[object.name='restoreJobCli'][property.name='listFiles']",
          message: "Job item listings page through JobResultsDialog; add a view instead of reading the listing here.",
        },
      ],
    },
  },
  {
    files: ["src/components/feedback.tsx"],
    rules: { "no-restricted-syntax": "off" },
  },
];
