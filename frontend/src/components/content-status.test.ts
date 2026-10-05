// @vitest-environment node

import { describe, expect, it } from "vitest";
import { FileContentSummary, OriginalAvailability as Original } from "@/entity";
import { archiveIndicator, archiveSummary, contentTime } from "./content-status";
const present = { originalAvailability: Original.PRESENT, signatureKnown: true, currentObservationValid: true };
const history = { hasVersions: true, restorableVersionCopyCount: 1n, latestVersionId: 9n, latestVersionRestorableCopyCount: 1n };
describe("Content availability matrix", () => {
  it.each([
    ["current archived content", { ...present, restorableCurrentCopyCount: 1n }, "success", undefined],
    ["local only", present, "warning", undefined],
    ["unbacked changes with history", { ...present, ...history }, "success", "changed"],
    ["unknown content with history", { ...present, ...history, currentObservationValid: false }, "success", "unknown"],
    ["unknown content without history", { ...present, currentObservationValid: false }, "info", undefined],
    [
      "unknown content with unusable history",
      { ...present, ...history, restorableVersionCopyCount: 0n, latestVersionRestorableCopyCount: 0n, currentObservationValid: false },
      "info",
      "warning",
    ],
    ["missing original with history", { originalAvailability: Original.MISSING, ...history }, "archive", undefined],
    ["unlinked original with history", { originalAvailability: Original.UNLINKED, ...history }, "archive", undefined],
    [
      "missing latest but older version available",
      { originalAvailability: Original.MISSING, ...history, latestVersionRestorableCopyCount: 0n },
      "archive",
      "warning",
    ],
    [
      "all unavailable versions",
      { originalAvailability: Original.MISSING, ...history, restorableVersionCopyCount: 0n, latestVersionRestorableCopyCount: 0n },
      "error",
      "warning",
    ],
    ["missing original without history", { originalAvailability: Original.MISSING }, "error", undefined],
    ["unlinked without history", { originalAvailability: Original.UNLINKED }, "error", undefined],
    ["local with unusable copies", { ...present, unhealthyCopyCount: 1n }, "warning", "warning"],
    ["unavailable original with archive", { originalAvailability: Original.UNAVAILABLE, ...history }, "info", undefined],
    ["unchecked original with archive", { originalAvailability: Original.UNCHECKED, ...history }, "info", undefined],
    ["some bad current copies", { ...present, restorableCurrentCopyCount: 1n, unhealthyCopyCount: 1n }, "success", "warning"],
    ["some bad historic copies", { originalAvailability: Original.UNLINKED, ...history, unhealthyVersionCopyCount: 1n }, "archive", "warning"],
  ] as const)("%s", (_, fields, tone, marker) => {
    const state = FileContentSummary.create(fields);
    expect(archiveSummary(state).tone).toBe(tone);
    expect(archiveSummary(state).marker?.kind).toBe(marker);
    expect(archiveIndicator(state).label).toBe(archiveSummary(state).title);
  });
  it("does not treat stale facts or ineligible Position counts as current coverage", () => {
    expect(
      archiveSummary(FileContentSummary.create({ ...present, currentObservationValid: false, archivedCopyCount: 3n, restorableCurrentCopyCount: 3n })).tone,
    ).toBe("info");
    expect(archiveSummary(FileContentSummary.create({ ...present, archivedCopyCount: 3n, healthyCopyCount: 3n })).tone).toBe("warning");
  });
  it("prioritizes anomalies without losing changed-content detail", () => {
    const result = archiveSummary(FileContentSummary.create({ ...present, ...history, unhealthyVersionCopyCount: 1n }));
    expect(result.marker?.kind).toBe("warning");
    expect(result.description).toContain("Changes not archived");
  });
  it("does not expire the historical observation", () => {
    const old = FileContentSummary.create({ ...present, restorableCurrentCopyCount: 1n, observedAtNs: 1000000n });
    expect(archiveSummary(old)).toEqual(archiveSummary({ ...old, observedAtNs: BigInt(Date.now()) * 1_000_000n }));
  });
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
