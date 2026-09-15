import { describe, expect, it } from "vitest";
import { FileScope, FileSelection, FileVersion, RestoreVersionMatch, RestoreVersionResolution } from "@/entity";
import { followRestorePolicy, restoreCutoff, restoreVersionLabel } from "./restore-version-policy";
import type { SelectionEntry } from "./selection-waitlist";

describe("Restore version policy", () => {
  it("converts valid local date input without inventing a cutoff for latest or invalid input", () => {
    expect(restoreCutoff({ mode: "before", date: "2026-09-01T18:30" })).toBe(BigInt(new Date("2026-09-01T18:30").getTime()));
    expect(restoreCutoff({ mode: "before", date: "" })).toBeUndefined();
    expect(restoreCutoff({ mode: "before", date: "not-a-date" })).toBeUndefined();
    expect(restoreCutoff({ mode: "latest", date: "2026-09-01T18:30" })).toBeUndefined();
  });
  it("resets custom versions to one File selection while preserving directory roots", () => {
    const root = FileSelection.create({ target: { oneofKind: "library", library: { fileId: 1n } }, scope: FileScope.SAVED });
    const folder = { key: FileSelection.toJsonString(root), name: "Photos", path: "Photos", isDir: true, selection: root };
    const versions = [10n, 11n].map((id) => ({
      key: `version:${id}`,
      name: "image.jpg",
      path: "Photos/image.jpg",
      fileID: "2",
      version: FileVersion.create({ id, fileId: 2n }),
    }));
    const result = followRestorePolicy([folder, ...versions]);
    expect(result).toHaveLength(2);
    expect(result[0]).toBe(folder);
    expect(result[1].version).toBeUndefined();
    expect(result[1].selection).toEqual(FileSelection.create({ target: { oneofKind: "library", library: { fileId: 2n } }, scope: FileScope.SAVED }));
  });
  it.each([
    [RestoreVersionMatch.RESTORE_VERSION_NO_SAVED_VERSION, "No saved version"],
    [RestoreVersionMatch.RESTORE_VERSION_AFTER_CUTOFF, "No backup at or before selected time"],
    [RestoreVersionMatch.RESTORE_VERSION_DATE_UNKNOWN, "Backup date unknown"],
  ])("keeps unmatched status %s distinct", (match, expected) => {
    expect(restoreVersionLabel({ key: "file:2", name: "test", path: "test" }, RestoreVersionResolution.create({ match }))).toBe(expected);
  });
  it("uses the resolved backup occurrence, not the version's latest repeat", () => {
    const resolution = RestoreVersionResolution.create({ version: { lastArchivedAtMs: 1790000000000n }, archivedAtMs: 1780000000000n });
    expect(restoreVersionLabel({ key: "file:2", name: "test", path: "test" }, resolution, 1785000000000n)).toContain(
      new Date(1780000000000).toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }),
    );
  });
  it("does not classify repeated old content as after the cutoff", () => {
    const entry: SelectionEntry = {
      key: "version:2",
      name: "test",
      path: "test",
      version: FileVersion.create({ firstArchivedAtMs: 100n, lastArchivedAtMs: 300n }),
    };
    expect(restoreVersionLabel(entry, undefined, 200n)).not.toContain("After selected time");
    expect(restoreVersionLabel({ ...entry, version: FileVersion.create({ firstArchivedAtMs: 300n }) }, undefined, 200n)).toContain("After selected time");
  });
});
