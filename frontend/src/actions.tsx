import { FileData, FileArray, FileAction } from "@samuelncui/chonky";
import { ChonkyActions, ChonkyIconName, defineFileAction } from "@samuelncui/chonky";

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

export const CutFilesAction = defineFileAction({
  id: "cut_files",
  requiresSelection: true,
  hotkeys: ["ctrl+x", "command+x"],
  button: { name: "Cut", toolbar: true, contextMenu: true, group: "Actions", icon: ChonkyIconName.copy },
});
export const PasteFilesAction = defineFileAction({
  id: "paste_files",
  hotkeys: ["ctrl+v", "command+v"],
  button: { name: "Paste", toolbar: true, contextMenu: true, group: "Actions", icon: ChonkyIconName.copy },
});
export const AddLocationFileAction = defineFileAction({
  id: "admit_location_files",
  requiresSelection: true,
  fileFilter: (file) => file?.isRegularFile === true,
  button: { name: "Add to Library", toolbar: true, contextMenu: true, group: "Actions", icon: ChonkyIconName.file },
});
export const ScanFilesAction = defineFileAction({
  id: "scan_files",
  requiresSelection: true,
  button: { name: "Scan", toolbar: true, contextMenu: true, group: "Actions", icon: ChonkyIconName.search },
});

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
  button: { name: "Refresh", tooltip: "Refresh", toolbar: true, contextMenu: true, icon: "mui-refresh", iconOnly: true },
});

export const EditFileMetadataAction = defineFileAction({
  id: "edit_file_metadata",
  requiresSelection: true,
  button: {
    name: "Edit tags & note",
    toolbar: true,
    contextMenu: true,
    group: "Actions",
    icon: "mui-edit-metadata",
  },
} as FileAction);

export const ArchiveLibraryAction = defineFileAction({
  id: "archive_library_selection",
  requiresSelection: true,
  button: { name: "Back up selection", toolbar: true, contextMenu: true, group: "Actions", icon: "mui-fiber-new" },
} as FileAction);

export const ImportPositionsAction = defineFileAction({
  id: "import_archive_positions",
  requiresSelection: true,
  fileFilter: (file) => !!file?.positionID && !file.isDir && file.signature instanceof Uint8Array && file.signature.length > 0,
  button: { name: "Add to Library", toolbar: true, contextMenu: true, group: "Actions", icon: "mui-fiber-new" },
} as FileAction);

export const LocateInOtherPaneAction = defineFileAction({
  id: "locate_in_other_pane",
  requiresSelection: true,
  button: {
    name: "Locate in Other Pane",
    toolbar: true,
    contextMenu: true,
    group: "Actions",
    icon: ChonkyIconName.folderOpen,
  },
} as FileAction);

export const ViewFileDetailsAction = defineFileAction({
  id: "view_file_details",
  requiresSelection: true,
  fileFilter: (file) => file?.detailsAvailable === true,
  button: {
    name: "View Details",
    toolbar: true,
    contextMenu: true,
    group: "Actions",
    icon: ChonkyIconName.info,
  },
} as FileAction);

export const TrimLibraryAction = defineFileAction({
  id: "trim_library",
  button: {
    name: "Clean up Library",
    toolbar: true,
    icon: "mui-cleaning",
  },
} as FileAction);

export const InitializeVolumeAction = defineFileAction({
  id: "initialize_volume",
  button: {
    name: "Add Volume",
    toolbar: true,
    icon: "mui-fiber-new",
  },
} as FileAction);

export const InspectMediaAction = defineFileAction({
  id: "inspect_media",
  requiresSelection: true,
  fileFilter: (file) => file?.isMedia === true,
  button: {
    name: "Properties",
    toolbar: true,
    contextMenu: true,
    group: "Actions",
    icon: "mui-data-usage",
  },
} as FileAction);

export const LoadMoreAction = defineFileAction({
  id: "load_more",
  button: {
    name: "Load More",
    toolbar: true,
    icon: "mui-refresh",
  },
} as FileAction);

export const ScanMediaAction = defineFileAction({
  id: "scan_media",
  requiresSelection: true,
  fileFilter: (file) => file?.isMedia === true,
  button: {
    name: "Scan",
    toolbar: true,
    contextMenu: true,
    group: "Actions",
    icon: "mui-data-usage",
  },
} as FileAction);

export const VerifyMediaAction = defineFileAction({
  id: "verify_media",
  requiresSelection: true,
  fileFilter: (file) => file?.isMedia === true,
  button: { name: "Check integrity", toolbar: true, contextMenu: true, group: "Actions", icon: "mui-data-usage" },
});
export const MediaJobsAction = defineFileAction({
  id: "media_jobs",
  requiresSelection: true,
  fileFilter: (file) => file?.isMedia === true,
  button: { name: "Related Jobs", toolbar: true, contextMenu: true, group: "Actions", icon: "mui-data-usage" },
});

export const DeleteMediaAction = defineFileAction({
  ...ChonkyActions.DeleteFiles,
  fileFilter: (file) => file?.isMedia === true,
  button: { ...ChonkyActions.DeleteFiles.button, name: "Remove from Library" },
} as FileAction);

export const ViewArchiveCopiesAction = defineFileAction({
  ...ViewFileDetailsAction,
  fileFilter: (file) => typeof file?.positionID === "bigint" && !file.isDir,
  button: { ...ViewFileDetailsAction.button, name: "Archived copies" },
} as FileAction);
