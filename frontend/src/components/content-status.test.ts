import { describe, expect, it } from "vitest";
import { FileContentSummary, Location, OnlineBinding, OriginalAvailability as Original } from "@/entity";
import { backupIndicator, backupSummary, contentTime, locationSetupLabel } from "./content-status";
const present = { originalAvailability: Original.ORIGINAL_PRESENT, signatureKnown: true, currentObservationValid: true };
const history = { hasVersions: true, restorableVersionCopies: 1n, latestVersionId: 9n, latestVersionRestorableCopies: 1n };
describe("Content availability matrix", () => {
  it.each([
    ["current archived content", { ...present, restorableCurrentCopies: 1n }, "success", undefined],
    ["local only", present, "warning", undefined],
    ["unbacked changes with history", { ...present, ...history }, "success", "changed"],
    ["unknown content with history", { ...present, ...history, currentObservationValid: false }, "success", "unknown"],
    ["unknown content without history", { ...present, currentObservationValid: false }, "info", undefined],
    [
      "unknown content with unusable history",
      { ...present, ...history, restorableVersionCopies: 0n, latestVersionRestorableCopies: 0n, currentObservationValid: false },
      "info",
      "warning",
    ],
    ["missing original with history", { originalAvailability: Original.ORIGINAL_MISSING, ...history }, "archive", undefined],
    ["unlinked original with history", { originalAvailability: Original.ORIGINAL_UNLINKED, ...history }, "archive", undefined],
    [
      "missing latest but older version available",
      { originalAvailability: Original.ORIGINAL_MISSING, ...history, latestVersionRestorableCopies: 0n },
      "archive",
      "warning",
    ],
    [
      "all unavailable versions",
      { originalAvailability: Original.ORIGINAL_MISSING, ...history, restorableVersionCopies: 0n, latestVersionRestorableCopies: 0n },
      "error",
      "warning",
    ],
    ["missing original without history", { originalAvailability: Original.ORIGINAL_MISSING }, "error", undefined],
    ["unlinked without history", { originalAvailability: Original.ORIGINAL_UNLINKED }, "error", undefined],
    ["local with unusable copies", { ...present, unhealthyCopies: 1n }, "warning", "warning"],
    ["unavailable original with backup", { originalAvailability: Original.ORIGINAL_UNAVAILABLE, ...history }, "info", undefined],
    ["unchecked original with backup", { originalAvailability: Original.ORIGINAL_UNCHECKED, ...history }, "info", undefined],
    ["some bad current copies", { ...present, restorableCurrentCopies: 1n, unhealthyCopies: 1n }, "success", "warning"],
    ["some bad historic copies", { originalAvailability: Original.ORIGINAL_UNLINKED, ...history, unhealthyVersionCopies: 1n }, "archive", "warning"],
  ] as const)("%s", (_, fields, tone, marker) => {
    const state = FileContentSummary.create(fields);
    expect(backupSummary(state).tone).toBe(tone);
    expect(backupSummary(state).marker?.kind).toBe(marker);
    expect(backupIndicator(state).label).toBe(backupSummary(state).title);
  });
  it("does not treat stale facts or ineligible Position counts as current coverage", () => {
    expect(backupSummary(FileContentSummary.create({ ...present, currentObservationValid: false, archivedCopies: 3n, restorableCurrentCopies: 3n })).tone).toBe(
      "info",
    );
    expect(backupSummary(FileContentSummary.create({ ...present, archivedCopies: 3n, healthyCopies: 3n })).tone).toBe("warning");
  });
  it("prioritizes anomalies without losing changed-content detail", () => {
    const result = backupSummary(FileContentSummary.create({ ...present, ...history, unhealthyVersionCopies: 1n }));
    expect(result.marker?.kind).toBe("warning");
    expect(result.description).toContain("Changes not backed up");
  });
  it("does not expire the historical observation", () => {
    const old = FileContentSummary.create({ ...present, restorableCurrentCopies: 1n, observedAtMs: 1n });
    expect(backupSummary(old)).toEqual(backupSummary({ ...old, observedAtMs: BigInt(Date.now()) }));
  });
  it("keeps imported confirmation separate from full scanning", () => {
    expect(locationSetupLabel(Location.create({ binding: OnlineBinding.CONFIRMED }))).toBe("Confirmed");
    expect(locationSetupLabel(Location.create({ binding: OnlineBinding.UNCONFIRMED }))).toBe("Review imported path");
    expect(contentTime(undefined, "Archive date unknown")).toBe("Archive date unknown");
  });
});
