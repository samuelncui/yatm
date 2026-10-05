import type { FileData } from "@samuelncui/chonky";
import { filesCli } from "@/api";
import { dateFromNs } from "@/tools/time";
import { archiveColors, type ArchiveMarker, type ArchiveTone } from "./content-status";
import {
  EntryKind,
  FileOperationKind,
  FileOperationRef,
  FileSelection,
  FileScope,
  FilesArchive,
  FilesCoverage,
  FilesInclude,
  FilesIssue,
  LocationEntryRef,
  OriginalAvailability,
  type FilesEntry,
} from "@/entity";

export const allowsFileOperation = (file: FileData | null | undefined, kind: FileOperationKind): boolean =>
  !!file && Array.isArray(file.allowedOperations) && file.allowedOperations.includes(kind);
export const filesEntryLibraryID = (entry?: FilesEntry): bigint | undefined =>
  entry?.reference?.target.oneofKind === "fileId" ? entry.reference.target.fileId : entry?.associatedFileId;

function filesStatusIndicator(status: FilesEntry["status"]) {
  if (!status) return undefined;
  const original = status.original;
  const present = original === OriginalAvailability.PRESENT;
  const missing = original === OriginalAvailability.MISSING || original === OriginalAvailability.UNLINKED;
  const archived = status.archive === FilesArchive.AVAILABLE;
  const currentKnown = status.current === FilesCoverage.COVERED || status.current === FilesCoverage.UNCOVERED;
  let tone: ArchiveTone = "info";
  if (missing && status.archive !== FilesArchive.UNSPECIFIED) tone = archived ? "archive" : "error";
  if (present && archived) tone = "success";
  if (present && status.archive === FilesArchive.NONE && currentKnown) tone = "warning";

  let issue = "";
  if (status.issues.includes(FilesIssue.ALL_COPIES_UNAVAILABLE)) issue = "Saved copies unavailable";
  else if (status.issues.includes(FilesIssue.LATEST_VERSION_UNAVAILABLE)) issue = "Latest version unavailable";
  else if (status.issues.includes(FilesIssue.PARTIAL_COPIES_UNAVAILABLE)) issue = "Some copies need attention";
  let marker: ArchiveMarker | undefined;
  if (issue) marker = { kind: "warning", color: archiveColors.warning, label: issue };
  else if (present && status.current === FilesCoverage.UNCOVERED && archived)
    marker = { kind: "changed", color: archiveColors.warning, label: "Changes not archived" };
  else if (present && status.current === FilesCoverage.UNSPECIFIED)
    marker = { kind: "unknown", color: archiveColors.info, label: "Current content not checked" };

  const originalLabels = {
    [OriginalAvailability.UNSPECIFIED]: "Local file not checked",
    [OriginalAvailability.UNCHECKED]: "Local file not checked",
    [OriginalAvailability.PRESENT]: "Local file present",
    [OriginalAvailability.MISSING]: "Local file missing",
    [OriginalAvailability.UNLINKED]: "No local file linked",
    [OriginalAvailability.UNAVAILABLE]: "Local file unavailable",
  };
  const archiveLabel = archived
    ? present && status.current === FilesCoverage.COVERED
      ? "Current content archived"
      : "Archive available"
    : status.archive === FilesArchive.NONE
      ? "No usable archive"
      : "Archive not checked";
  return { color: archiveColors[tone], label: [originalLabels[original], archiveLabel, marker?.label].filter(Boolean).join(" · "), marker };
}

export function filesEntryData(entry: FilesEntry, index?: number): FileData {
  if (entry.error) {
    return {
      // Error rows have no operation identity. Their position distinguishes escaped display names.
      id: `unavailable:${index ?? entry.path}`,
      name: entry.name,
      isDir: entry.kind === EntryKind.DIRECTORY,
      isHidden: entry.name.startsWith("."),
      isSymlink: entry.kind === EntryKind.LINK,
      openable: false,
      selectable: false,
      draggable: false,
      droppable: false,
      dndOpenable: false,
      detailsAvailable: false,
      allowedOperations: [],
      details: [entry.error],
      status: { color: archiveColors.error, label: entry.error },
    };
  }
  if (!entry.reference) throw new Error("File reference is missing");
  const target = entry.reference.target;
  const isDir = entry.kind === EntryKind.DIRECTORY;
  const location = target.oneofKind === "location" ? target.location : undefined;
  const operations =
    target.oneofKind === "fileId" && entry.kind === EntryKind.FILE && entry.status?.original === OriginalAvailability.UNLINKED
      ? entry.operations.filter((operation) => operation !== FileOperationKind.ARCHIVE)
      : entry.operations;
  const id = target.oneofKind === "fileId" ? String(target.fileId) : `location${isDir ? "" : "-file:" + location?.locationId}:${entry.path}`;
  return {
    id,
    name: entry.name,
    isDir,
    isHidden: entry.name.startsWith("."),
    isSymlink: entry.kind === EntryKind.LINK,
    isRegularFile: entry.kind === EntryKind.FILE,
    size: entry.sizeBytes === undefined ? undefined : Number(entry.sizeBytes),
    modDate: dateFromNs(entry.mtimeNs),
    modDateNs: entry.mtimeNs,
    openable: true,
    selectable: true,
    draggable: entry.operations.includes(FileOperationKind.MOVE),
    droppable: isDir && entry.operations.includes(FileOperationKind.MKDIR),
    detailsAvailable: true,
    libraryFileID: filesEntryLibraryID(entry),
    operationReference: entry.reference,
    allowedOperations: operations,
    status: !isDir ? filesStatusIndicator(entry.status) : undefined,
    ...(location
      ? {
          physicalLocationID: String(location.locationId),
          physicalPath: entry.path,
          locationReference: location,
          originSelection: FileSelection.create({
            target: { oneofKind: "location", location: { locationId: location.locationId, path: entry.path } },
            scope: FileScope.ALL,
          }),
        }
      : {}),
  };
}

export const libraryDirectoryReference = (id: string) => FileOperationRef.create({ target: { oneofKind: "fileId", fileId: BigInt(id) } });
export const locationDirectoryReference = (id: string, path: string) =>
  FileOperationRef.create({ target: { oneofKind: "location", location: LocationEntryRef.create({ locationId: BigInt(id), path }) } });

const browseIncludes = [FilesInclude.ATTRIBUTES, FilesInclude.STATUS, FilesInclude.OPERATIONS, FilesInclude.NAVIGATION];

// DirectoryBatch carries one transport batch's rows and the directory facts available so far.
export type DirectoryBatch = {
  files: ReturnType<typeof filesEntryData>[];
  first: boolean;
  total?: bigint;
  scope: FileScope;
  directory?: FilesEntry;
  breadcrumbs: FilesEntry[];
};

// listDirectory reads one directory completely. The server enumerates it once and streams
// the listing, so there is no cursor to follow and no page to continue: the first batch
// states the total and the later ones only add rows. `onBatch` receives every batch and the
// read's growing accumulator. Copy that accumulator before publishing an intermediate
// snapshot; the returned array can be used directly once the stream ends.
export async function listDirectory(
  directory: FileOperationRef,
  scope: FileScope,
  onBatch?: (batch: DirectoryBatch, files: readonly FileData[]) => void,
  signal?: AbortSignal,
) {
  signal?.throwIfAborted();
  const call = filesCli.list({ directory, scope, batchSize: 500, include: browseIncludes }, { abort: signal });
  const files: ReturnType<typeof filesEntryData>[] = [];
  let total: bigint | undefined;
  let resolvedScope = scope;
  let directoryEntry: FilesEntry | undefined;
  let breadcrumbs: FilesEntry[] = [];
  let first = true;
  for await (const batch of call.responses) {
    signal?.throwIfAborted();
    const rows = batch.entries.map((entry, index) => filesEntryData(entry, files.length + index));
    files.push(...rows);
    if (batch.totalEntryCount !== undefined) total = batch.totalEntryCount;
    resolvedScope = batch.scope;
    directoryEntry = batch.directory ?? directoryEntry;
    breadcrumbs = batch.breadcrumbs.length ? batch.breadcrumbs : breadcrumbs;
    onBatch?.({ files: rows, first, total, scope: resolvedScope, directory: directoryEntry, breadcrumbs }, files);
    first = false;
  }
  signal?.throwIfAborted();
  return { files, total, scope: resolvedScope, directory: directoryEntry, breadcrumbs };
}

// searchFiles answers one bounded page of a query, which is what keeps a cursor.
export async function searchFiles(directory: FileOperationRef, scope: FileScope, cursor = "", query = "", recursive = false) {
  const reply = await filesCli.search({
    directory,
    scope,
    cursor,
    limit: 100,
    query,
    recursive,
    include: browseIncludes,
  }).response;
  return {
    files: reply.entries.map((entry) => ({ ...filesEntryData(entry), ...(recursive ? { name: entry.path || entry.name } : {}) })),
    nextCursor: reply.nextCursor,
    total: reply.totalEntryCount,
    scope: reply.scope,
    directory: reply.directory,
    breadcrumbs: reply.breadcrumbs,
  };
}

// filesPage answers one page-shaped request. A directory read now returns the whole
// listing at once, so only a query or a recursive sweep still continues through a cursor.
export async function filesPage(directory: FileOperationRef, scope: FileScope, cursor = "", query = "", recursive = false) {
  if (query || recursive) return searchFiles(directory, scope, cursor, query, recursive);
  const listing = await listDirectory(directory, scope);
  return { ...listing, nextCursor: "" };
}
