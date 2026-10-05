import type { FileData } from "@samuelncui/chonky";
import { FileScope, FileSelection } from "@/entity";
import { selectionForFile } from "./location-files";

export type ScanSelectionEntry = {
  selection: FileSelection;
  name: string;
  path: string;
  isDir?: boolean;
  unavailableReason?: string;
};

export function scanSelectionEntry(file: FileData, scope: FileScope, source: string, directory = ""): ScanSelectionEntry {
  const path = file.physicalPath === undefined ? [directory, file.name].filter(Boolean).join("/") : String(file.physicalPath);
  return { selection: selectionForFile(file, scope), name: file.name, path: `${source}/${path}`, isDir: !!file.isDir };
}

export const scanSelectionKey = ({ selection }: ScanSelectionEntry) => {
  const target = selection.target;
  if (target.oneofKind === "library") return JSON.stringify(["library", String(target.library.fileId), selection.scope]);
  if (target.oneofKind === "location") return JSON.stringify(["location", String(target.location.locationId), target.location.path, selection.scope]);
  return FileSelection.toJsonString(selection);
};
