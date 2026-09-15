import {
  EntryKind,
  File,
  FileKind,
  FileOperationKind,
  FileOperationRef,
  FilesEntry,
  ListFilesReply,
  FileScope,
  type FileGetReply,
  type ListLocationEntriesReply,
  type LocationEntry,
} from "@/entity";

const operations = [FileOperationKind.MOVE, FileOperationKind.DELETE, FileOperationKind.MAKE_DIRECTORY];
export function libraryEntry(file: File): FilesEntry {
  return FilesEntry.create({
    reference: { target: { oneofKind: "fileId", fileId: file.id } },
    file,
    name: file.name,
    path: file.name,
    kind: file.kind === FileKind.DIRECTORY || ((file.mode ?? 0n) & 0x80000000n) !== 0n ? EntryKind.ENTRY_DIRECTORY : EntryKind.ENTRY_FILE,
    size: file.size,
    summary: file.contentSummary,
    operations,
    canRead: true,
  });
}
export function libraryPage(reply: Partial<FileGetReply>) {
  return ListFilesReply.create({ entries: (reply.children ?? []).map(libraryEntry), scope: reply.scope ?? FileScope.ALL, nextCursor: reply.nextCursor ?? "" });
}
export function liveEntry(entry: LocationEntry, locationId: bigint): FilesEntry {
  const reference = entry.reference ?? { locationId, path: entry.path, bindingToken: "bound" };
  return FilesEntry.create({
    reference: { target: { oneofKind: "location", location: reference } },
    file: entry.file,
    path: entry.path,
    name: entry.path.replace(/\/$/, "").split("/").at(-1) ?? "",
    kind: entry.isDir ? EntryKind.ENTRY_DIRECTORY : EntryKind.ENTRY_FILE,
    size: entry.reference?.facts?.size,
    mtimeNs: entry.reference?.facts?.mtimeNs,
    summary: entry.file?.contentSummary,
    operations,
    canRead: !entry.isDir,
  });
}
export function livePage(reply: ListLocationEntriesReply, locationId: bigint) {
  return ListFilesReply.create({ entries: reply.entries.map((entry) => liveEntry(entry, locationId)), scope: FileScope.ALL, nextCursor: reply.nextCursor });
}
export function locationPageRequest(reference: FileOperationRef, cursor = "", query = "") {
  if (reference.target.oneofKind !== "location") throw new Error("Expected a Location directory");
  const location = reference.target.location;
  return { locationId: location.locationId, parentPath: location.path ? location.path.replace(/\/$/, "") + "/" : "", cursor, limit: 100, nameFilter: query };
}
