import type { FileData } from "@samuelncui/chonky";
import { filesCli } from "@/api";
import { FileOperationKind, FileScope, FileSelection, FileVersion, type FilesDetail } from "@/entity";
import { allowsFileOperation, filesEntryData, filesEntryLibraryID, libraryDirectoryReference } from "@/components/files-browser";
import { associatedLibraryFileID, selectionForFile } from "@/components/location-files";

export { loadSelectionEntries, saveSelectionEntries, selectionStorageKey, mergeSelectionEntries } from "@/state/selections";
export type { SelectionEntry, SelectionKind, SelectionMergeResult } from "@/state/selections";
import type { SelectionEntry, SelectionKind, SelectionMergeResult } from "@/state/selections";

export function selectionAddMessage(kind: SelectionKind, result: Omit<SelectionMergeResult, "entries">): string {
  const label = kind === "archive" ? "Archive" : "Restore";
  if (!result.added) {
    if (result.keptExplicit)
      return result.keptExplicit === 1
        ? "This File already has an explicit version in the Restore list."
        : "These Files already have explicit versions in the Restore list.";
    return result.duplicates === 1 ? `This item is already in the ${label} list.` : `These items are already in the ${label} list.`;
  }
  const added = `${result.added} ${result.added === 1 ? "item" : "items"} added to the ${label} list.`;
  if (!result.replacedAutomatic) return added;
  const replaced = `${result.replacedAutomatic} automatic ${result.replacedAutomatic === 1 ? "selection was" : "selections were"} replaced by the explicit version.`;
  return `${added} ${replaced}`;
}

export const canAddFileToSelection = (kind: SelectionKind, file: FileData | null | undefined): boolean =>
  !!file && (kind === "archive" ? allowsFileOperation(file, FileOperationKind.ARCHIVE) : !!file.isDir || !!associatedLibraryFileID(file));

async function fileDetail(fileID: string): Promise<FilesDetail> {
  const detail = (await filesCli.get({ reference: libraryDirectoryReference(fileID) }).response).detail;
  if (!detail?.entry) throw new Error("File details are unavailable. Refresh Files and try again.");
  return detail;
}

export async function selectionEntriesForFiles(kind: SelectionKind, files: FileData[], scope: FileScope, sourceName: string): Promise<SelectionEntry[]> {
  const entries: SelectionEntry[] = [];
  for (const file of files) {
    if (kind === "archive" && !allowsFileOperation(file, FileOperationKind.ARCHIVE)) {
      throw new Error(`${file.name} has no eligible current original to add to the Archive list.`);
    }
    const fileID = associatedLibraryFileID(file);
    if (kind === "restore" && !file.isDir) {
      if (!fileID) throw new Error(`${file.name} has no saved File in Library to add to the Restore list.`);
      const selection = FileSelection.create({ target: { oneofKind: "library", library: { fileId: BigInt(fileID) } }, scope: FileScope.SAVED });
      const detail = await fileDetail(fileID);
      const target = detail.organization?.path || detail.entry?.path || file.name;
      entries.push({ key: FileSelection.toJsonString(selection), name: file.name, path: target, target, fileID, selection });
      continue;
    }

    const selection = selectionForFile(file, kind === "restore" ? FileScope.SAVED : scope);
    let target: string | undefined;
    if (fileID) {
      const detail = await fileDetail(fileID);
      target = detail.organization?.path || detail.entry?.path || file.name;
    }
    entries.push({
      key: FileSelection.toJsonString(selection),
      name: file.name,
      path: file.physicalPath !== undefined ? `${sourceName}/${file.physicalPath}` : (target ?? file.name),
      target,
      selection,
      fileID: file.isDir ? undefined : fileID,
      isDir: file.isDir,
    });
  }
  return entries;
}

export function archiveEntryForDetail(detail: FilesDetail): SelectionEntry {
  if (!detail.entry) throw new Error("File details are unavailable. Refresh Files and try again.");
  const file = filesEntryData(detail.entry);
  if (!allowsFileOperation(file, FileOperationKind.ARCHIVE)) {
    throw new Error("This item has no eligible current original to add to the Archive list.");
  }
  const selection = selectionForFile(file, FileScope.DEFAULT);
  const target = detail.organization?.path || detail.entry.path || detail.entry.name;
  const sourceName = detail.original?.sourceName || "Location";
  return {
    key: FileSelection.toJsonString(selection),
    name: detail.entry.name,
    path: file.physicalPath !== undefined ? `${sourceName}/${file.physicalPath}` : target,
    target,
    selection,
    fileID: file.isDir ? undefined : associatedLibraryFileID(file),
    isDir: file.isDir,
  };
}

export function restoreVersionEntry(detail: FilesDetail, version: FileVersion): SelectionEntry {
  if (!detail.entry) throw new Error("File details are unavailable. Refresh Files and try again.");
  if (version.fileId <= 0n) throw new Error("This saved version has no File identity.");
  const detailFileID = filesEntryLibraryID(detail.entry);
  if (detailFileID !== undefined && detailFileID !== version.fileId) throw new Error("This saved version does not belong to the selected File.");
  const fileID = String(version.fileId);
  const target = detail.organization?.path || detail.entry.path || detail.entry.name;
  return {
    key: `version:${version.id}`,
    name: detail.entry.name || target,
    path: target,
    target,
    fileID,
    version,
  };
}

export async function loadRestoreVersionEntry(version: FileVersion): Promise<SelectionEntry> {
  return restoreVersionEntry(await fileDetail(String(version.fileId)), version);
}
