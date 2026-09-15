import { describe, expect, it } from "vitest";
import { formatFilesize } from "./tools";

describe("Catalog byte formatting", () => {
  it("accepts bigint counters directly without coercing their type", () => {
    expect(formatFilesize(0n)).toBe("0 B");
    expect(formatFilesize(1024n)).toBe("1 KB");
    expect(formatFilesize(2n ** 60n)).toBe("1 EB");
    expect(formatFilesize(1024)).toBe("1 KB");
  });
});
