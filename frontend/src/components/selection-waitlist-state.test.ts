import { beforeEach, describe, expect, it } from "vitest";
import { FileScope, FileSelection, FileVersion } from "@/entity";
import { loadSelectionEntries, mergeSelectionEntries, saveSelectionEntries, selectionStorageKey, type SelectionEntry } from "./selection-waitlist-state";

const automatic = (fileID = "7"): SelectionEntry => {
  const selection = FileSelection.create({
    target: { oneofKind: "library", library: { fileId: BigInt(fileID) } },
    scope: FileScope.SAVED,
  });
  return {
    key: FileSelection.toJsonString(selection),
    name: "image.jpg",
    path: "Pictures/image.jpg",
    target: "Pictures/image.jpg",
    fileID,
    selection,
  };
};

const explicit = (id = 12n, fileID = 7n): SelectionEntry => ({
  key: `version:${id}`,
  name: "image.jpg",
  path: "Pictures/image.jpg",
  target: "Pictures/image.jpg",
  fileID: String(fileID),
  version: FileVersion.create({ id, fileId: fileID, sizeBytes: 55n }),
});

import { createAppStore } from "@/state/store";
import { addSelectionEntries } from "@/state/selections";
beforeEach(() => sessionStorage.clear());

describe("selection waitlist state", () => {
  it("round-trips automatic selections and exact FileVersion IDs through the browser session", () => {
    saveSelectionEntries("restore", [automatic(), explicit()]);

    const restored = loadSelectionEntries("restore");
    expect(restored).toHaveLength(2);
    expect(restored[0].selection?.target).toEqual({ oneofKind: "library", library: { fileId: 7n } });
    expect(restored[1].version?.id).toBe(12n);
    expect(restored[1].version?.fileId).toBe(7n);
  });

  it("keeps the existing Restore conflict rules for automatic and explicit versions", () => {
    const chosen = mergeSelectionEntries([automatic()], [explicit()]);
    expect(chosen.added).toBe(1);
    expect(chosen.replacedAutomatic).toBe(1);
    expect(chosen.entries).toEqual([explicit()]);

    const automaticAgain = mergeSelectionEntries(chosen.entries, [automatic()]);
    expect(automaticAgain.added).toBe(0);
    expect(automaticAgain.keptExplicit).toBe(1);
    expect(automaticAgain.entries).toBe(chosen.entries);
  });

  it("deduplicates repeated additions while retaining other explicit versions of the same File", () => {
    const store = createAppStore();
    const add = (entries: SelectionEntry[]) => addSelectionEntries(store, "restore", entries);
    const first = add([explicit(12n)]);
    const duplicate = add([explicit(12n)]);
    const another = add([explicit(13n)]);

    expect(first.added).toBe(1);
    expect(duplicate).toMatchObject({ added: 0, duplicates: 1 });
    expect(another.added).toBe(1);
    expect(loadSelectionEntries("restore").map((entry) => entry.version?.id)).toEqual([12n, 13n]);
  });

  it("merges a large mixed selection without rescanning all existing File IDs per addition", () => {
    let identityReads = 0;
    const item = (id: number, version = false) => ({
      key: `${version ? "version" : "automatic"}:${id}`,
      get fileID() {
        identityReads++;
        return String(id);
      },
      selection: version ? undefined : true,
      version: version || undefined,
    });
    const current = Array.from({ length: 2000 }, (_, id) => item(id));
    const additions = Array.from({ length: 1000 }, (_, id) => item(id, true));
    const result = mergeSelectionEntries(current, additions);
    expect(result).toMatchObject({ added: 1000, replacedAutomatic: 1000, duplicates: 0, keptExplicit: 0 });
    expect(result.entries).toHaveLength(2000);
    expect(current).toHaveLength(2000);
    expect(result.entries.slice(0, 1000).every((entry) => !entry.version)).toBe(true);
    expect(result.entries.slice(1000).every((entry) => entry.version)).toBe(true);
    expect(identityReads).toBeLessThanOrEqual(5 * (current.length + additions.length));
  });

  it("preserves the list across a simulated navigation or refresh and ignores invalid session data", () => {
    addSelectionEntries(createAppStore(), "archive", [automatic()]);
    expect(loadSelectionEntries("archive")).toHaveLength(1);

    sessionStorage.setItem(selectionStorageKey("restore"), "not-json");
    expect(loadSelectionEntries("restore")).toEqual([]);
  });
});
