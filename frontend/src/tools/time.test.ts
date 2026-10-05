// @vitest-environment node

import { describe, expect, it } from "vitest";
import { dateFromNs, dateToNs, maxUnixNs, minUnixNs, parseUnixNs } from "./time";

describe("Unix nanosecond display and input boundary", () => {
  it.each([
    [1_700_000_000_123_999_999n, "2023-11-14T22:13:20.123Z"],
    [0n, "1970-01-01T00:00:00.000Z"],
    [-1n, "1969-12-31T23:59:59.999Z"],
    [-1_000_001n, "1969-12-31T23:59:59.998Z"],
    [minUnixNs, "1677-09-21T00:12:43.145Z"],
    [maxUnixNs, "2262-04-11T23:47:16.854Z"],
  ])("renders %s without rounding across a millisecond", (ns, date) => {
    expect(dateFromNs(ns)?.toISOString()).toBe(date);
    expect(parseUnixNs(String(ns))).toBe(ns);
  });

  it.each([
    ["2023-11-14T22:13:20.123Z", 1_700_000_000_123_000_000n],
    ["1969-12-31T23:59:59.999Z", -1_000_000n],
    ["1970-01-01T00:00:00.000Z", 0n],
    ["1677-09-21T00:12:43.146Z", -9_223_372_036_854_000_000n],
    ["2262-04-11T23:47:16.854Z", 9_223_372_036_854_000_000n],
  ])("converts a Date edit at %s using bigint arithmetic", (date, ns) => {
    expect(dateToNs(new Date(date))).toBe(ns);
  });

  it("rejects missing, malformed and out-of-range instants without inventing an epoch", () => {
    expect(dateFromNs()).toBeUndefined();
    for (const ns of [minUnixNs - 1n, maxUnixNs + 1n]) {
      expect(dateFromNs(ns)).toBeUndefined();
      expect(parseUnixNs(String(ns))).toBeUndefined();
    }
    for (const input of ["", " 1", "1.5", "1e9", "not-a-date"]) expect(parseUnixNs(input)).toBeUndefined();
    for (const input of ["invalid", "1677-09-21T00:12:43.145Z", "2262-04-11T23:47:16.855Z"]) expect(dateToNs(new Date(input))).toBeUndefined();
  });
});
