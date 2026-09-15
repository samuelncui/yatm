import { beforeEach, describe, expect, it, vi } from "vitest";
import { EntryKind, FileContentSummary, FileOperationKind, FilesEntry, FileScope, OriginalAvailability } from "@/entity";
import { filesEntryData, filesPage, inspectFilePage, libraryDirectoryReference, locationDirectoryReference } from "./files-browser";

const { list, get, inspect, collect } = vi.hoisted(() => ({ list: vi.fn(), get: vi.fn(), inspect: vi.fn(), collect: vi.fn() }));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  filesCli: { list, get, inspect, collect },
}));
beforeEach(() => vi.clearAllMocks());
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const entry = () =>
  FilesEntry.create({
    reference: {
      target: {
        oneofKind: "location",
        location: { locationId: 2n, path: "report.txt", bindingToken: "root-token", facts: { identity: "native-object", size: 7n, mode: 420, mtimeNs: 1n } },
      },
    },
    name: "report.txt",
    path: "report.txt",
    kind: EntryKind.ENTRY_FILE,
    operations: [FileOperationKind.MOVE],
    size: 7n,
  });

describe("Shared Files browser boundary", () => {
  it("browses either source through the same paginated pure request", async () => {
    list.mockReturnValue(call({ entries: [entry()], nextCursor: "next", scope: FileScope.ALL }));
    for (const directory of [libraryDirectoryReference("42"), locationDirectoryReference("2", "reports")]) {
      const page = await filesPage(directory, FileScope.ALL, "cursor", "tag:annual");
      expect(list).toHaveBeenLastCalledWith({
        directory,
        scope: FileScope.ALL,
        cursor: "cursor",
        limit: 100,
        nameFilter: "",
        query: "tag:annual",
        needSize: false,
      });
      expect(page.nextCursor).toBe("next");
    }
    expect(collect).not.toHaveBeenCalled();
  });
  it("preserves guarded uncollected references without inventing a Library identity", () => {
    const value = entry();
    const row = filesEntryData(value);
    expect(row.libraryFileID).toBeUndefined();
    expect(row.operationReference).toEqual(value.reference);
    expect(row.id).not.toBe("2");
    expect(row.allowedOperations).toEqual([FileOperationKind.MOVE]);
  });
  it("inspects only files and derives the same status from observed facts", async () => {
    const value = entry();
    const row = filesEntryData(value);
    const directory = filesEntryData(FilesEntry.create({ ...value, name: "folder", path: "folder", kind: EntryKind.ENTRY_DIRECTORY }));
    const summary = FileContentSummary.create({ originalAvailability: OriginalAvailability.ORIGINAL_PRESENT, hasVersions: true, restorableVersionCopies: 1n });
    inspect.mockReturnValue(call({ observations: [{ reference: value.reference, summary }] }));
    const rows = await inspectFilePage([row, directory]);
    expect(inspect).toHaveBeenCalledWith({ references: [value.reference] });
    expect(rows[0].contentSummary).toEqual(summary);
    expect(rows[0].status?.label).toContain("Backup available");
    expect(rows[1]).toBe(directory);
  });
});
