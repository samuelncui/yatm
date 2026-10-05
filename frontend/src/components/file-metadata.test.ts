import { describe, expect, it } from "vitest";
import { normalizeTags, tagDelta, unicodeLength, validateNote, validateTags } from "./file-metadata";

describe("File metadata rules", () => {
  it("normalizes whitespace and case, removes duplicates and keeps the first occurrence", () => {
    expect(normalizeTags([" PHOTO ", "", "photo", " Ä ", "ä", "🦊"])).toEqual(["photo", "ä", "🦊"]);
    expect(tagDelta([" KEEP ", "remove"], ["keep", "ADDED", "added"])).toEqual({ addTags: ["added"], removeTags: ["remove"] });
  });
  it("counts Unicode code points at the tag and note boundaries", () => {
    expect(unicodeLength("🦊")).toBe(1);
    expect(validateTags(["🦊".repeat(128)], [])).toBe("");
    expect(validateTags(["🦊".repeat(129)], [])).toBe("Each tag can have at most 128 characters.");
    expect(validateNote("🦊".repeat(4096))).toBe("");
    expect(validateNote("🦊".repeat(4097))).toBe("A note can have at most 4096 characters.");
  });
  it("limits added tags rather than the accumulated tag set", () => {
    const baseline = Array.from({ length: 25 }, (_, index) => `old-${index}`);
    const additions = Array.from({ length: 16 }, (_, index) => `new-${index}`);
    const tags = [...baseline, ...additions];
    expect(validateTags(tags, tagDelta(baseline, tags).addTags)).toBe("");
    expect(validateTags([...tags, "extra"], [...additions, "extra"])).toBe("You can add at most 16 tags at once.");
  });
});
