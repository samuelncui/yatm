import { FileData, FileArray, FileAction } from "chonky";
import { defineFileAction } from "chonky";

type RenameFileState = {
  contextMenuTriggerFile: FileData;
  instanceId: string;
  selectedFiles: FileArray;
  selectedFilesForAction: FileArray;
};

export const RenameFileAction = defineFileAction({
  id: "rename_file",
  requiresSelection: true,
  button: {
    name: "Rename File",
    toolbar: true,
    contextMenu: true,
    group: "Actions",
    icon: "edit",
  },
  __extraStateType: {} as RenameFileState,
} as FileAction);

export const RefreshListAction = defineFileAction({
  id: "refresh_list",
} as FileAction);
