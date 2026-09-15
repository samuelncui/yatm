import { unstable_ClassNameGenerator as ClassNameGenerator } from "@mui/material/className";

// Configure before any component imports cache utility classes used in internal CSS selectors.
ClassNameGenerator.configure((componentName: string) => `app-${componentName}`);
