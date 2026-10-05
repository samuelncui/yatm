import { beforeEach, describe, expect, it, vi } from "vitest";
import { EntryKind, FileOperationKind, FilesEntry, FileScope, FilesInclude, FilesArchive, FilesCoverage, FilesIssue, OriginalAvailability } from "@/entity";
import { filesEntryData, filesPage, listDirectory, libraryDirectoryReference, locationDirectoryReference } from "./files-browser";
import { listingStream as stream } from "@/test/files-fixture";
import { archiveColors } from "./content-status";

const { list, search, get } = vi.hoisted(() => ({ list: vi.fn(), search: vi.fn(), get: vi.fn() }));
vi.mock("@/api", () => ({ filesCli: { list, search, get } }));
beforeEach(() => vi.clearAllMocks());
const entry = () =>
  FilesEntry.create({
    reference: {
      target: {
        oneofKind: "location",
        location: { locationId: 2n, path: "report.txt", facts: { sizeBytes: 7n, mode: 420, mtimeNs: 1n } },
      },
    },
    name: "report.txt",
    path: "report.txt",
    kind: EntryKind.FILE,
    operations: [FileOperationKind.MOVE],
    sizeBytes: 7n,
  });
describe("Shared Files browser boundary", () => {
  it.each([
    [undefined, undefined],
    [0n, "1970-01-01T00:00:00.000Z"],
    [-1n, "1969-12-31T23:59:59.999Z"],
    [1_700_000_000_123_999_999n, "2023-11-14T22:13:20.123Z"],
  ])("preserves optional mtime %s and its exact sort value", (mtimeNs, date) => {
    const row = filesEntryData(FilesEntry.create({ ...entry(), mtimeNs }));
    expect(row.modDateNs).toBe(mtimeNs);
    expect(row.modDate).toEqual(date === undefined ? undefined : new Date(date));
  });

  it("retains failed rows across batches without turning their display names into operation references", async () => {
    const failed = FilesEntry.create({ name: "bad\\xff", path: "bad\\xff", error: "Unsupported filename: invalid UTF-8" });
    list.mockReturnValue(
      stream(
        { entries: [entry(), failed], totalEntryCount: 3n, scope: FileScope.ALL, breadcrumbs: [] },
        { entries: [failed], scope: FileScope.ALL, breadcrumbs: [] },
      ),
    );
    const result = await listDirectory(locationDirectoryReference("2", ""), FileScope.ALL);
    expect(result.total).toBe(3n);
    expect(result.files).toHaveLength(3);
    expect(new Set(result.files.map((file) => file.id)).size).toBe(3);
    expect(result.files[0].selectable).toBe(true);
    for (const row of result.files.slice(1)) {
      expect(row).toMatchObject({
        name: failed.name,
        openable: false,
        selectable: false,
        draggable: false,
        droppable: false,
        dndOpenable: false,
        detailsAvailable: false,
        allowedOperations: [],
        details: [failed.error],
        status: { color: archiveColors.error, label: failed.error },
      });
      for (const key of ["operationReference", "locationReference", "originSelection", "libraryFileID", "size", "modDate"]) expect(row[key]).toBeUndefined();
    }
  });

  it("still rejects a malformed successful entry without a reference", () => {
    expect(() => filesEntryData(FilesEntry.create({ name: "missing-reference" }))).toThrow("File reference is missing");
  });

  it.each([libraryDirectoryReference("42"), locationDirectoryReference("2", "reports")])(
    "consumes a complete directory beyond 100,000 rows in one stream: %s",
    async (directory) => {
      const total = 100001;
      list.mockReturnValue({
        responses: (async function* () {
          for (let offset = 0; offset < total; offset += 500) {
            yield {
              entries: Array.from({ length: Math.min(500, total - offset) }, (_, index) =>
                FilesEntry.create({
                  reference:
                    directory.target.oneofKind === "location"
                      ? locationDirectoryReference("2", `reports/file-${offset + index + 1}`)
                      : libraryDirectoryReference(String(offset + index + 1)),
                  name: `file-${offset + index + 1}`,
                  kind: EntryKind.FILE,
                }),
              ),
              totalEntryCount: offset === 0 ? BigInt(total) : undefined,
              scope: FileScope.ALL,
              breadcrumbs: [],
            };
          }
        })(),
      });
      const onBatch = vi.fn();
      const result = await listDirectory(directory, FileScope.ALL, onBatch);

      expect(list).toHaveBeenCalledOnce();
      expect(search).not.toHaveBeenCalled();
      expect(result.total).toBe(100001n);
      expect(result.files).toHaveLength(total);
      expect(result.files.at(-1)?.name).toBe("file-100001");
      expect(onBatch).toHaveBeenCalledTimes(201);
      expect(onBatch.mock.calls[0][0]).toMatchObject({ first: true, total: 100001n });
      expect(onBatch.mock.calls.at(-1)?.[0]).toMatchObject({ first: false, total: 100001n });
    },
  );

  it("publishes a directory batch before the stream finishes and rejects a later read failure", async () => {
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => (finish = resolve));
    list.mockReturnValue({
      responses: (async function* () {
        yield { entries: [entry()], totalEntryCount: 2n, scope: FileScope.ALL, breadcrumbs: [] };
        await pending;
        throw new Error("Directory unavailable");
      })(),
    });
    const onBatch = vi.fn();
    const result = listDirectory(locationDirectoryReference("2", "reports"), FileScope.ALL, onBatch);
    const failure = expect(result).rejects.toThrow("Directory unavailable");
    await vi.waitFor(() => expect(onBatch).toHaveBeenCalledOnce());
    expect(onBatch.mock.calls[0][0]).toMatchObject({ first: true, total: 2n, files: [expect.objectContaining({ name: "report.txt" })] });
    finish();
    await failure;
    expect(list).toHaveBeenCalledOnce();
  });

  it("shares one accumulator while retaining independent batch rows", async () => {
    list.mockReturnValue(stream(...Array.from({ length: 3 }, () => ({ entries: [entry()], scope: FileScope.ALL, breadcrumbs: [] }))));
    const lengths: number[] = [];
    const onBatch = vi.fn((_batch, files) => lengths.push(files.length));
    const result = await listDirectory(libraryDirectoryReference("42"), FileScope.ALL, onBatch);
    expect(lengths).toEqual([1, 2, 3]);
    for (const [batch, files] of onBatch.mock.calls) {
      expect(batch.files).toHaveLength(1);
      expect(files).toBe(result.files);
    }
  });

  it("passes cancellation to the transport and rejects late rows before projecting them", async () => {
    const controller = new AbortController();
    list.mockReturnValue({
      responses: (async function* () {
        yield { entries: [entry()], scope: FileScope.ALL, breadcrumbs: [] };
        controller.abort();
        // A cancelled transport must not let even a malformed late row be projected.
        yield { entries: [FilesEntry.create()], scope: FileScope.ALL, breadcrumbs: [] };
      })(),
    });
    const onBatch = vi.fn();
    await expect(listDirectory(libraryDirectoryReference("42"), FileScope.ALL, onBatch, controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(list.mock.calls[0][1]).toEqual({ abort: controller.signal });
    expect(onBatch).toHaveBeenCalledOnce();
    list.mockClear();
    await expect(listDirectory(libraryDirectoryReference("42"), FileScope.ALL, onBatch, controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(list).not.toHaveBeenCalled();
  });

  it.each([
    [OriginalAvailability.PRESENT, FilesArchive.AVAILABLE, FilesCoverage.COVERED, "success", undefined],
    [OriginalAvailability.PRESENT, FilesArchive.AVAILABLE, FilesCoverage.UNCOVERED, "success", "changed"],
    [OriginalAvailability.PRESENT, FilesArchive.AVAILABLE, FilesCoverage.UNSPECIFIED, "success", "unknown"],
    [OriginalAvailability.MISSING, FilesArchive.AVAILABLE, FilesCoverage.NOT_APPLICABLE, "archive", undefined],
    [OriginalAvailability.UNLINKED, FilesArchive.AVAILABLE, FilesCoverage.NOT_APPLICABLE, "archive", undefined],
    [OriginalAvailability.UNCHECKED, FilesArchive.AVAILABLE, FilesCoverage.UNSPECIFIED, "info", undefined],
    [OriginalAvailability.UNAVAILABLE, FilesArchive.AVAILABLE, FilesCoverage.UNSPECIFIED, "info", undefined],
    [OriginalAvailability.PRESENT, FilesArchive.NONE, FilesCoverage.UNCOVERED, "warning", undefined],
    [OriginalAvailability.PRESENT, FilesArchive.NONE, FilesCoverage.UNSPECIFIED, "info", "unknown"],
    [OriginalAvailability.PRESENT, FilesArchive.UNSPECIFIED, FilesCoverage.UNSPECIFIED, "info", "unknown"],
    [OriginalAvailability.MISSING, FilesArchive.NONE, FilesCoverage.NOT_APPLICABLE, "error", undefined],
  ] as const)("preserves the status matrix for original=%s, archive=%s, coverage=%s", (original, archive, current, tone, marker) => {
    const row = filesEntryData(FilesEntry.create({ ...entry(), status: { original, archive, current } }));
    expect(row.status?.color).toBe(archiveColors[tone]);
    expect(row.status?.marker?.kind).toBe(marker);
  });

  it.each([FilesCoverage.COVERED, FilesCoverage.UNCOVERED, FilesCoverage.UNSPECIFIED])("prioritizes copy issues over coverage=%s", (current) => {
    const row = filesEntryData(
      FilesEntry.create({
        ...entry(),
        status: {
          original: OriginalAvailability.PRESENT,
          archive: FilesArchive.AVAILABLE,
          current,
          issues: [FilesIssue.PARTIAL_COPIES_UNAVAILABLE],
        },
      }),
    );
    expect(row.status?.color).toBe(archiveColors.success);
    expect(row.status?.marker).toEqual({ kind: "warning", color: archiveColors.warning, label: "Some copies need attention" });
  });

  it("requests every page once, including status and navigation, without per-row details", async () => {
    list.mockReturnValue(stream({ entries: [entry()], totalEntryCount: 1n, scope: FileScope.ALL, breadcrumbs: [] }));
    for (const directory of [libraryDirectoryReference("42"), locationDirectoryReference("2", "reports")]) {
      search.mockReturnValue({ response: Promise.resolve({ entries: [entry()], nextCursor: "next", scope: FileScope.ALL, breadcrumbs: [] }) });
      const page = await filesPage(directory, FileScope.ALL, "cursor", "tag:annual");
      expect(search).toHaveBeenLastCalledWith({
        directory,
        scope: FileScope.ALL,
        cursor: "cursor",
        limit: 100,
        query: "tag:annual",
        recursive: false,
        include: [FilesInclude.ATTRIBUTES, FilesInclude.STATUS, FilesInclude.OPERATIONS, FilesInclude.NAVIGATION],
      });
      expect(page.nextCursor).toBe("next");
    }
    expect(search).toHaveBeenCalledTimes(2);
    expect(get).not.toHaveBeenCalled();
  });
  it("preserves guarded unassociated references and server operations", () => {
    const value = entry();
    const row = filesEntryData(value);
    expect(row.libraryFileID).toBeUndefined();
    expect(row.operationReference).toEqual(value.reference);
    expect(row.id).not.toBe("2");
    expect(row.allowedOperations).toEqual([FileOperationKind.MOVE]);
    expect(filesEntryData(FilesEntry.create({ ...value, operations: [] })).draggable).toBe(false);
  });
  it("hides Archive for an unlinked Library File while allowing directories and live Location files", () => {
    const archive = FileOperationKind.ARCHIVE;
    const saved = FilesEntry.create({
      ...entry(),
      reference: libraryDirectoryReference("42"),
      status: { original: OriginalAvailability.UNLINKED },
      operations: [archive, FileOperationKind.UPDATE_METADATA],
    });
    expect(filesEntryData(saved).allowedOperations).not.toContain(archive);
    expect(filesEntryData(FilesEntry.create({ ...saved, kind: EntryKind.DIRECTORY })).allowedOperations).toContain(archive);
    expect(filesEntryData(FilesEntry.create({ ...saved, status: { original: OriginalAvailability.PRESENT } })).allowedOperations).toContain(archive);
    expect(filesEntryData(FilesEntry.create({ ...saved, reference: entry().reference })).allowedOperations).toContain(archive);
  });
  it("renders server status without a follow-up inspection or a directory aggregate", () => {
    const value = FilesEntry.create({
      ...entry(),
      status: { original: OriginalAvailability.PRESENT, archive: FilesArchive.AVAILABLE, current: FilesCoverage.COVERED, issues: [] },
    });
    expect(filesEntryData(value).status?.label).toContain("Current content archived");
    value.status!.issues = [FilesIssue.ALL_COPIES_UNAVAILABLE];
    expect(filesEntryData(value).status?.marker?.label).toBe("Saved copies unavailable");
    value.kind = EntryKind.DIRECTORY;
    expect(filesEntryData(value).status).toBeUndefined();
    expect(get).not.toHaveBeenCalled();
  });
  it("passes recursive search through Search and propagates page failures", async () => {
    search.mockReturnValue({ response: Promise.reject(new Error("Offline")) });
    await expect(filesPage(libraryDirectoryReference("42"), FileScope.ALL, "", "name:report", true)).rejects.toThrow("Offline");
    expect(search.mock.calls[0][0].recursive).toBe(true);
    expect(get).not.toHaveBeenCalled();
  });
});
