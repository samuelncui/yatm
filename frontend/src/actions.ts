import { FileData, FileArray, FileAction } from "@samuelncui/chonky";
import { defineFileAction } from "@samuelncui/chonky";
import { ChonkyActions } from "@samuelncui/chonky";

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

export const AddFileAction = defineFileAction({
  id: "add_file",
  __payloadType: ChonkyActions.EndDragNDrop.__payloadType,
} as FileAction);

export const CreateBackupJobAction = defineFileAction({
  id: "create_backup_job",
  button: {
    name: "Create Backup Job",
    toolbar: true,
  },
} as FileAction);

export const CreateRestoreJobAction = defineFileAction({
  id: "create_restore_job",
  button: {
    name: "Create Restore Job",
    toolbar: true,
  },
} as FileAction);

export const TrimLibraryAction = defineFileAction({
  id: "trim_library",
  button: {
    name: "Trim Library",
    toolbar: true,
  },
} as FileAction);
