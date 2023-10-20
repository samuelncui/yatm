import { FileData, FileArray, FileAction } from "@samuelncui/chonky";
import { ChonkyActions, defineFileAction } from "@samuelncui/chonky";

type RenameFileState = {
  contextMenuTriggerFile: FileData;
  instanceId: string;
  selectedFiles: FileArray;
  selectedFilesForAction: FileArray;
};

export const CreateFolder = defineFileAction({
  ...ChonkyActions.CreateFolder,
  button: {
    ...ChonkyActions.CreateFolder.button,
    // iconOnly: true,
  },
} as FileAction);

export const RenameFileAction = defineFileAction({
  id: "rename_file",
  requiresSelection: true,
  button: {
    name: "Rename File",
    toolbar: true,
    contextMenu: true,
    group: "Actions",
    icon: "mui-rename",
  },
  __extraStateType: {} as RenameFileState,
} as FileAction);

export const GetDataUsageAction = defineFileAction({
  id: "get_data_usage",
  button: {
    name: "Data Usage",
    toolbar: true,
    icon: "mui-data-usage",
    // iconOnly: true,
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
    icon: "mui-fiber-new",
  },
} as FileAction);

export const CreateRestoreJobAction = defineFileAction({
  id: "create_restore_job",
  button: {
    name: "Create Restore Job",
    toolbar: true,
    icon: "mui-fiber-new",
  },
} as FileAction);

export const TrimLibraryAction = defineFileAction({
  id: "trim_library",
  button: {
    name: "Trim Library",
    toolbar: true,
    icon: "mui-cleaning",
  },
} as FileAction);
