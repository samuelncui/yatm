// @vitest-environment node

import { describe, expect, it } from "vitest";
import { contentTime } from "./content-status";
describe("Content time", () => {
  it("keeps an unknown time explicit instead of inventing an observation", () => {
    expect(contentTime(undefined, "Archive date unknown")).toBe("Archive date unknown");
  });
  it.each([
    [1_700_000_000_123_999_999n, "2023-11-14T22:13:20.123Z"],
    [0n, "1970-01-01T00:00:00.000Z"],
    [-1n, "1969-12-31T23:59:59.999Z"],
  ])("formats ns %s once, including the epoch and negative instants", (ns, date) => {
    expect(contentTime(ns)).toBe(
      new Date(date).toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }),
    );
  });
});
