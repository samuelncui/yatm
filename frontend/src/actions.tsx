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
  // Rename applies to one item; hide it for a multi-item selection (Hidden = 0, Disabled = 1, Default = 2).
  customVisibility: (state) => {
    const count = state.selectedFilesForAction.length;
    return count > 1 ? 0 : count === 1 ? 2 : 1;
  },
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
    tooltip: "Data Usage",
    toolbar: true,
    icon: "mui-data-usage",
    iconOnly: true,
  },
  __extraStateType: {} as RenameFileState,
} as FileAction);

// The tooltip state while a measurement runs; Chonky resolves one text per action id.
export const CancelDataUsageAction = defineFileAction({
  ...GetDataUsageAction,
  id: "cancel_data_usage",
  button: { ...GetDataUsageAction.button, tooltip: "Cancel Data Usage" },
} as FileAction);

export const RefreshListAction = defineFileAction({
  id: "refresh_list",
  button: { name: "Refresh", tooltip: "Refresh", toolbar: true, icon: "mui-refresh", iconOnly: true },
});

// One file is edited in its details; the menu offers batch editing for a multi-file selection.
export const EditFileMetadataAction = defineFileAction({
  id: "edit_file_metadata",
  requiresSelection: true,
  // Chonky exports this enum as a type only: Hidden = 0, Default = 2.
  customVisibility: (state) => (state.selectedFilesForAction.length > 1 ? 2 : 0),
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
  button: { name: "Archive selection", toolbar: true, contextMenu: true, group: "Actions", icon: "mui-fiber-new" },
} as FileAction);

// The Files browser names Archive selection as a list action; the id keeps its translation separate.
export const AddToArchiveListAction = defineFileAction({
  ...ArchiveLibraryAction,
  id: "add_to_archive_list",
  button: { ...ArchiveLibraryAction.button, name: "Add to Archive list", tooltip: "Add to Archive list" },
} as FileAction);

export const RestoreLibraryAction = defineFileAction({
  id: "restore_library_selection",
  requiresSelection: true,
  button: { name: "Add to Restore list", toolbar: true, contextMenu: true, group: "Actions", icon: "mui-fiber-new" },
} as FileAction);

export const ImportPositionsAction = defineFileAction({
  id: "import_archive_positions",
  requiresSelection: true,
  fileFilter: (file) => !!file?.position && (!!file.isDir || (file.position.signature instanceof Uint8Array && file.position.signature.length > 0)),
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
  id: "remove_media_from_library",
  fileFilter: (file) => file?.isMedia === true,
  button: { ...ChonkyActions.DeleteFiles.button, name: "Remove from Library" },
} as FileAction);
