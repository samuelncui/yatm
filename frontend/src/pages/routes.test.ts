// @vitest-environment node

import { describe, expect, it } from "vitest";
import { jobListPath } from "./routes";

describe("jobListPath", () => {
  it("keeps a list origin and falls back to the plain list for anything else", () => {
    expect(jobListPath("/jobs?kind=1&location=8")).toBe("/jobs?kind=1&location=8");
    expect(jobListPath("/settings/locations/8?tab=jobs")).toBe("/jobs");
    expect(jobListPath("https://example.com/jobs")).toBe("/jobs");
    expect(jobListPath(undefined)).toBe("/jobs");
  });
});
