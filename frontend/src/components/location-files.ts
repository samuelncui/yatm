import type { FileData } from "@samuelncui/chonky";
import { convertFiles, filesCli, locationCli, type LibraryFileData } from "@/api";
import { FileOperationRef, FileSelection, FileScope, LocationEntryRef, type Location, type LocationEntry } from "@/entity";
import { filesPage, locationDirectoryReference } from "@/components/files-browser";

export const locationDirectoryID = (path: string) => `location:${path}`;
export const locationPath = (id: string) => (id.startsWith("location:") ? id.slice("location:".length) : "");

export function locatedFile(file: FileData, locationId: bigint, path: string, revision: bigint, reference?: LocationEntryRef): FileData {
  const name = path.replace(/\/$/, "").split("/").at(-1) ?? path;
  return {
    ...file,
    name,
    isHidden: name.startsWith("."),
    draggable: !!reference,
    droppable: !!reference && !!file.isDir,
    openable: true,
    selectable: true,
    physicalPath: path,
    physicalLocationID: String(locationId),
    locationReference: reference,
    originSelection: FileSelection.create({
      target: { oneofKind: "location", location: { locationId, path, revision, reference } },
      scope: FileScope.ALL,
    }),
  };
}

export function locationEntryFile(entry: LocationEntry, location: Location, revision = location.revision): FileData {
  const name = entry.path.replace(/\/$/, "").split("/").at(-1) ?? entry.path;
  const known = entry.file ? convertFiles([entry.file])[0] : undefined;
  const facts = entry.reference?.facts;
  return locatedFile(
    {
      ...known,
      id: entry.isDir ? locationDirectoryID(entry.path) : `location-file:${location.id}:${entry.path}`,
      name,
      isDir: entry.isDir,
      isSymlink: !!facts && (facts.mode & 0x08000000) !== 0,
      // Go FileMode type bits: directory, link, device, pipe, socket, character device and irregular.
      isRegularFile: !!facts && (facts.mode & 0x8f280000) === 0,
      size: entry.isDir ? undefined : facts ? Number(facts.size) : undefined,
      modDate: facts ? new Date(Number(facts.mtimeNs / 1000000n)) : undefined,
      detailsAvailable: true,
      libraryFileID: entry.file?.id,
      status: known?.status,
    },
    location.id,
    entry.path,
    revision,
    entry.reference,
  );
}

export function associatedLibraryFileID(file: FileData): string | undefined {
  if (file.physicalLocationID) return typeof file.libraryFileID === "bigint" ? String(file.libraryFileID) : undefined;
  return /^\d+$/.test(file.id) ? file.id : undefined;
}

export function fileLocationReference(file: FileData): LocationEntryRef {
  if (!LocationEntryRef.is(file.locationReference)) throw new Error("File observation is missing. Refresh this folder.");
  return LocationEntryRef.clone(file.locationReference);
}

export async function admitLocationFile(file: FileData): Promise<LibraryFileData> {
  if (!file.physicalLocationID) return file as LibraryFileData;
  const reply = await filesCli.collect({
    references: [FileOperationRef.create({ target: { oneofKind: "location", location: fileLocationReference(file) } })],
    automatic: false,
  }).response;
  const admitted = reply.entries[0]?.file;
  if (!admitted) throw new Error("Could not add this file to Library");
  return convertFiles([admitted])[0];
}

// Share identical in-flight reads, without retaining completed live observations.
const pendingPages = new Map<string, ReturnType<typeof readLocationFilePage>>();

export function locationFilePage(id: string, path: string, cursor = "", nameFilter = "") {
  const parentPath = path ? path.replace(/\/$/, "") + "/" : "";
  const key = JSON.stringify([id, parentPath, cursor, nameFilter]);
  const pending = pendingPages.get(key);
  if (pending) return pending;
  const request = readLocationFilePage(id, parentPath, cursor, nameFilter).finally(() => pendingPages.delete(key));
  pendingPages.set(key, request);
  return request;
}

async function readLocationFilePage(id: string, parentPath: string, cursor: string, nameFilter: string) {
  const location = (await locationCli.get({ id: BigInt(id), revision: 0n }).response).location;
  if (!location) throw new Error("Location no longer exists");
  const reply = await filesPage(locationDirectoryReference(id, parentPath.replace(/\/$/, "")), FileScope.ALL, cursor, nameFilter);
  return { location, files: reply.files, nextCursor: reply.nextCursor, revision: location.revision, collectionError: "" };
}

export function locationBreadcrumbs(location: Pick<Location, "id" | "name">, path: string): FileData[] {
  const names = path.split("/").filter(Boolean);
  const paths = ["", ...names.map((_, index) => names.slice(0, index + 1).join("/"))];
  return paths.map((path, index) => ({
    id: locationDirectoryID(path),
    name: index ? names[index - 1] : location.name,
    isDir: true,
    draggable: false,
    droppable: true,
    physicalLocationID: String(location.id),
    physicalPath: path,
  }));
}

export function selectionForFile(file: FileData, scope: FileScope): FileSelection {
  if (file.originSelection !== undefined) {
    if (!FileSelection.is(file.originSelection)) throw new Error("File has an invalid selection. Refresh this folder.");
    return FileSelection.clone(file.originSelection);
  }
  if (file.physicalLocationID !== undefined) throw new Error("File has no live selection. Refresh this folder.");
  return FileSelection.create({ target: { oneofKind: "library", library: { fileId: BigInt(file.id) } }, scope });
}
