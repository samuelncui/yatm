import { expect, it } from "vitest";
import { FileScope, FileSelection, LocationEntryRef } from "@/entity";
import { scanSelectionEntry, scanSelectionKey } from "./scan-selection";

it("preserves the selected Library visibility scope and display path", () => {
  const entry = scanSelectionEntry({ id: "12", name: "Photos", isDir: true }, FileScope.SAVED, "Library", "Albums");
  expect(entry.path).toBe("Library/Albums/Photos");
  expect(entry.selection.scope).toBe(FileScope.SAVED);
  expect(entry.selection.target).toEqual({ oneofKind: "library", library: { fileId: 12n } });
  expect(scanSelectionKey(entry)).not.toBe(scanSelectionKey({ ...entry, selection: { ...entry.selection, scope: FileScope.ALL } }));
});

it("reselects the same live root by Location and path", () => {
  const reference = LocationEntryRef.create({ locationId: 3n, path: "Photos/a.jpg", facts: { sizeBytes: 10n } });
  const liveSelection = (observation: LocationEntryRef) =>
    FileSelection.create({
      scope: FileScope.ALL,
      target: { oneofKind: "location", location: { locationId: observation.locationId, path: observation.path } },
    });
  const file = { id: "live-file", name: "a.jpg", physicalPath: reference.path, originSelection: liveSelection(reference), physicalLocationID: "3" };
  const old = scanSelectionEntry(file, FileScope.ALL, "Camera");
  const latest = scanSelectionEntry(
    { ...file, originSelection: liveSelection({ ...reference, facts: { ...reference.facts!, sizeBytes: 20n } }) },
    FileScope.ALL,
    "Camera",
  );
  expect(scanSelectionKey(old)).toBe(scanSelectionKey(latest));
  expect(latest.path).toBe("Camera/Photos/a.jpg");
  expect(latest.selection).toEqual(old.selection);
});
