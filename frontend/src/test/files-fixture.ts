import {
  EntryKind,
  File,
  FileKind,
  FileOperationKind,
  FileOperationRef,
  FileOperationResult,
  FilesEntry,
  FilesDetail,
  GetFileResponse,
  FilesArchive,
  FilesCoverage,
  ListFilesResponse,
  RemoveFilesResponse,
  FileScope,
  type LocationEntriesPage,
  type LocationEntry,
} from "@/entity";

const operations = [
  FileOperationKind.MOVE,
  FileOperationKind.REMOVE,
  FileOperationKind.MKDIR,
  FileOperationKind.SCAN,
  FileOperationKind.ARCHIVE,
  FileOperationKind.ADMIT,
  FileOperationKind.UPDATE_METADATA,
];
export type FilePageFixture = { file?: File; children?: File[]; scope?: FileScope; nextCursor?: string };
export const operationResponse = (value: Parameters<typeof FileOperationResult.create>[0]) =>
  RemoveFilesResponse.create({ result: FileOperationResult.create(value) });
export function libraryEntry(file: File): FilesEntry {
  return FilesEntry.create({
    reference: { target: { oneofKind: "fileId", fileId: file.id } },
    name: file.name,
    path: file.name,
    kind: file.kind === FileKind.DIRECTORY || ((file.mode ?? 0n) & 0x80000000n) !== 0n ? EntryKind.DIRECTORY : EntryKind.FILE,
    sizeBytes: file.sizeBytes,
    status: file.contentSummary
      ? {
          original: file.contentSummary.originalAvailability,
          archive: file.contentSummary.restorableVersionCopyCount ? FilesArchive.AVAILABLE : FilesArchive.NONE,
          current: file.contentSummary.currentObservationValid ? FilesCoverage.COVERED : FilesCoverage.UNSPECIFIED,
          issues: [],
        }
      : undefined,
    operations,
  });
}
export function libraryPage(reply: FilePageFixture, id = 0n) {
  const root = libraryEntry(File.create({ id: 0n, name: "Library", kind: FileKind.DIRECTORY }));
  const directory = libraryEntry(reply.file ?? File.create({ id, name: id ? "Subfolder" : "Library", kind: FileKind.DIRECTORY }));
  return ListFilesResponse.create({
    entries: (reply.children ?? []).map(libraryEntry),
    directory,
    breadcrumbs: id ? [root, directory] : [root],
    scope: reply.scope ?? FileScope.ALL,
    totalEntryCount: BigInt(reply.children?.length ?? 0),
  });
}
export function liveEntry(entry: LocationEntry, locationId: bigint): FilesEntry {
  const reference = entry.reference ?? { locationId, path: entry.path };
  return FilesEntry.create({
    reference: { target: { oneofKind: "location", location: reference } },
    associatedFileId: entry.file?.id,
    path: entry.path,
    name: entry.path.replace(/\/$/, "").split("/").at(-1) ?? "",
    kind: entry.directory ? EntryKind.DIRECTORY : EntryKind.FILE,
    sizeBytes: entry.reference?.facts?.sizeBytes,
    mtimeNs: entry.reference?.facts?.mtimeNs,
    operations,
  });
}
export function libraryDetail(file: File) {
  return GetFileResponse.create({
    detail: FilesDetail.create({
      entry: libraryEntry(file),
      organization: { parent: { target: { oneofKind: "fileId", fileId: file.parentId } }, path: file.name, tags: file.tags, note: file.note },
    }),
  });
}
export function liveDetail(entry: LocationEntry, locationId: bigint) {
  const row = liveEntry(entry, locationId);
  return GetFileResponse.create({
    detail: FilesDetail.create({ entry: row, original: { reference: row.reference, sourceName: "Location", path: entry.path } }),
  });
}
export function livePage(reply: LocationEntriesPage, locationId: bigint, path = "") {
  const parts = path.split("/").filter(Boolean);
  const breadcrumbs = ["", ...parts.map((_, index) => parts.slice(0, index + 1).join("/"))].map((path, index) =>
    FilesEntry.create({
      reference: { target: { oneofKind: "location", location: { locationId, path } } },
      path,
      name: index ? parts[index - 1] : "Photos",
      kind: EntryKind.DIRECTORY,
      operations,
    }),
  );
  return ListFilesResponse.create({
    entries: reply.entries.map((entry) => liveEntry(entry, locationId)),
    directory: breadcrumbs.at(-1),
    breadcrumbs,
    scope: FileScope.ALL,
    totalEntryCount: BigInt(reply.entries.length),
  });
}
export function locationPageRequest(reference: FileOperationRef, cursor = "", query = "") {
  if (reference.target.oneofKind !== "location") throw new Error("Expected a Location directory");
  const location = reference.target.location;
  return { locationId: location.locationId, parentPath: location.path ? location.path.replace(/\/$/, "") + "/" : "", cursor, limit: 100, nameFilter: query };
}

// listingStream wraps one listing's batches as the stream the Files browser consumes.
export function listingStream(...batches: ListFilesResponse[]) {
  return {
    responses: (async function* () {
      for (const batch of batches) yield batch;
    })(),
  };
}

// streamListing adapts a paged listing mock to the stream the browser consumes: one
// reply becomes one batch, whether the mock returns a reply or a promise for it.
export function streamListing(reply: any) {
  return {
    responses: (async function* () {
      yield await reply;
    })(),
  };
}

// streamListingLater is streamListing for a mock that resolves its reply when a test
// releases it, so the stream can be handed back before the reply exists.
export function streamListingLater(reply: Promise<any>) {
  return {
    responses: (async function* () {
      yield await reply;
    })(),
  };
}
