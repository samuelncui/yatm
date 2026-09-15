import type { FileData } from "@samuelncui/chonky";
import { convertFiles, filesCli } from "@/api";
import { EntryKind, FileOperationKind, FileOperationRef, FileSelection, FileScope, LocationEntryRef, type FilesEntry } from "@/entity";
import { backupIndicator } from "@/components/content-status";

export function filesEntryData(entry: FilesEntry): FileData {
  if (!entry.reference) throw new Error("File reference is missing");
  const target = entry.reference.target;
  const isDir = entry.kind === EntryKind.ENTRY_DIRECTORY;
  const known = entry.file ? convertFiles([entry.file])[0] : undefined;
  const location = target.oneofKind === "location" ? target.location : undefined;
  const id = target.oneofKind === "fileId" ? String(target.fileId) : `location${isDir ? "" : "-file:" + location?.locationId}:${entry.path}`;
  return {
    ...known,
    id,
    name: entry.name,
    isDir,
    isHidden: entry.name.startsWith("."),
    isSymlink: entry.kind === EntryKind.ENTRY_LINK,
    isRegularFile: entry.kind === EntryKind.ENTRY_FILE,
    size: entry.size === undefined ? undefined : Number(entry.size),
    modDate: entry.mtimeNs ? new Date(Number(entry.mtimeNs / 1000000n)) : undefined,
    openable: true,
    selectable: true,
    draggable: entry.operations.includes(FileOperationKind.MOVE),
    droppable: isDir,
    detailsAvailable: true,
    libraryFileID: entry.file?.id,
    operationReference: entry.reference,
    allowedOperations: entry.operations,
    canRead: entry.canRead,
    contentSummary: entry.summary,
    status: !isDir && entry.summary ? backupIndicator(entry.summary) : undefined,
    ...(location
      ? {
          physicalLocationID: String(location.locationId),
          physicalPath: entry.path,
          locationReference: location,
          originSelection: FileSelection.create({
            target: { oneofKind: "location", location: { locationId: location.locationId, path: entry.path, reference: location, revision: 0n } },
            scope: FileScope.ALL,
          }),
        }
      : {}),
  };
}

export const libraryDirectoryReference = (id: string) => FileOperationRef.create({ target: { oneofKind: "fileId", fileId: BigInt(id) } });
export const locationDirectoryReference = (id: string, path: string) =>
  FileOperationRef.create({ target: { oneofKind: "location", location: LocationEntryRef.create({ locationId: BigInt(id), path }) } });

export async function filesPage(directory: FileOperationRef, scope: FileScope, cursor = "", query = "", needSize = false) {
  const reply = await filesCli.list({ directory, scope, cursor, limit: 100, nameFilter: "", query, needSize }).response;
  return { files: reply.entries.map(filesEntryData), nextCursor: reply.nextCursor, scope: reply.scope };
}

export async function inspectFilePage(files: FileData[]) {
  const references = files
    .filter((file) => !file.isDir)
    .map((file) => (FileOperationRef.is(file.operationReference) ? file.operationReference : libraryDirectoryReference(file.id)));
  if (!references.length) return files;
  const { observations } = await filesCli.inspect({ references }).response;
  const summaries = new Map(
    observations.filter((item) => item.reference && item.summary).map((item) => [FileOperationRef.toJsonString(item.reference!), item.summary!]),
  );
  return files.map((file) => {
    if (file.isDir) return file;
    const reference = FileOperationRef.is(file.operationReference) ? file.operationReference : libraryDirectoryReference(file.id);
    const summary = summaries.get(FileOperationRef.toJsonString(reference));
    return summary ? { ...file, contentSummary: summary, status: backupIndicator(summary) } : file;
  });
}
