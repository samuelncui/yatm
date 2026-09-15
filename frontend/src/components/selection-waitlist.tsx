import { type ReactNode, useMemo, useState } from "react";
import { Alert, Box, Button } from "@mui/material";
import { ThemeProvider, useTheme } from "@mui/material/styles";
import { toast } from "react-toastify";
import {
  ChonkyActions,
  FileBrowser,
  FileContextMenu,
  FileList,
  FileNavbar,
  FileToolbar,
  defineFileAction,
  type ChonkyFileActionData,
  type FileData,
} from "@samuelncui/chonky";
import type { FileSelection, FileVersion, RestoreVersionResolution } from "@/entity";
import { restoreVersionLabel } from "@/components/restore-version-policy";
import { useWaitlistDirectory } from "@/components/waitlist-directory";

export type SelectionEntry = {
  key: string;
  name: string;
  path: string;
  isDir?: boolean;
  selection?: FileSelection;
  version?: FileVersion;
  fileID?: string;
  target?: string;
  size?: number;
};
export const waitlistRootID = (kind: string) => `waitlist:${kind}`;
const RemoveSelection = defineFileAction({
  id: "remove_job_selection",
  requiresSelection: true,
  fileFilter: (file: FileData | null) => !!file?.explicitSelection,
  button: { name: "Remove from list", toolbar: true, contextMenu: true },
});
const ChangeVersion = defineFileAction({
  id: "change_selected_version",
  requiresSelection: true,
  fileFilter: (file: FileData | null) => !!file?.versionSelectable,
  button: { name: "Change version", toolbar: true, contextMenu: true },
});
const ClearSelection = defineFileAction({ id: "clear_job_selection", button: { name: "Clear list", toolbar: true } });

export const SelectionWaitlist = ({
  kind,
  label,
  entries,
  busy,
  onRemove,
  onClear,
  onChooseVersion,
  footer,
  resolutions,
  cutoff,
}: {
  kind: string;
  label: string;
  entries: SelectionEntry[];
  busy: boolean;
  onRemove: (keys: Set<string>) => void;
  onClear: () => void;
  onChooseVersion: (entry: SelectionEntry, version?: FileVersion) => void;
  footer?: ReactNode;
  resolutions?: RestoreVersionResolution[];
  cutoff?: bigint;
}) => {
  const theme = useTheme();
  const [trail, setTrail] = useState<SelectionEntry[]>([]);
  const activeTrail = entries.some((entry) => entry.key === trail[0]?.key) ? trail : [];
  const directory = activeTrail.at(-1);
  const browse = useWaitlistDirectory(directory, kind === "restore", cutoff);
  const automaticVersions = useMemo(
    () => new Map((directory ? browse.resolutions : resolutions)?.map((resolution) => [String(resolution.fileId), resolution])),
    [directory, browse.resolutions, resolutions],
  );
  const displayed = useMemo(() => {
    if (!directory) return entries;
    return browse.entries.flatMap((child) => {
      const overrides = entries.filter((entry) => entry.version && entry.fileID === child.fileID);
      if (!overrides.length) return [child];
      return overrides.map((entry) => ({ ...entry, path: child.path, name: child.name }));
    });
  }, [directory, entries, browse.entries]);
  const explicitKeys = useMemo(() => new Set(entries.map((entry) => entry.key)), [entries]);
  const files = useMemo(
    () =>
      displayed.map((entry): FileData => {
        const resolution = automaticVersions.get(entry.fileID ?? "");
        const version = entry.version ?? resolution?.version;
        return {
          id: entry.key,
          name: entry.name,
          isDir: entry.isDir,
          size: version ? Number(version.size) : entry.size,
          draggable: false,
          droppable: false,
          openable: !!entry.isDir || (kind === "restore" && !!entry.fileID),
          versionSelectable: kind === "restore" && !entry.isDir && !!entry.fileID,
          explicitSelection: !directory && explicitKeys.has(entry.key),
          details: [
            ...(entry.path !== entry.name ? [entry.path] : []),
            ...(kind === "restore" ? [!entry.isDir && !entry.fileID ? "No saved version" : restoreVersionLabel(entry, resolution, cutoff)] : []),
            ...(kind === "archive" && entry.target && entry.target !== entry.path ? [`Archive: ${entry.target}`] : []),
          ],
        };
      }),
    [displayed, kind, automaticVersions, cutoff, explicitKeys, directory],
  );
  const choose = (entry: SelectionEntry) => onChooseVersion(entry, entry.version ?? automaticVersions.get(entry.fileID ?? "")?.version);
  const action = (data: ChonkyFileActionData) => {
    if (busy) return;
    if (data.id === RemoveSelection.id) {
      onRemove(new Set(data.state.selectedFilesForAction.map((file) => file.id)));
      return;
    }
    if (data.id === ClearSelection.id) {
      onClear();
      return;
    }
    if (data.id === ChonkyActions.OpenFiles.id) {
      const file = data.payload.targetFile ?? data.payload.files[0];
      if (file?.id === waitlistRootID(kind)) {
        setTrail([]);
        return;
      }
      const ancestor = activeTrail.findIndex((entry) => entry.key === file?.id);
      if (ancestor >= 0) {
        setTrail(activeTrail.slice(0, ancestor + 1));
        return;
      }
      const entry = displayed.find((entry) => entry.key === file?.id);
      if (entry?.isDir) {
        setTrail([...activeTrail, entry]);
        return;
      }
      if (entry?.fileID && kind === "restore") choose(entry);
      return;
    }
    if (data.id === ChangeVersion.id) {
      if (data.state.selectedFilesForAction.length !== 1) {
        toast.info("Select one file to choose its version.");
        return;
      }
      const entry = displayed.find((entry) => entry.key === data.state.selectedFilesForAction[0].id);
      if (entry) choose(entry);
    }
  };
  return (
    <div className="selection-waitlist" aria-label={`${label} waitlist`}>
      <FileBrowser
        footer={
          footer != null && footer !== false ? (
            <ThemeProvider theme={theme}>
              <Box sx={{ display: "flex", flexDirection: "column", minHeight: 0, typography: "body1", color: "text.primary" }}>{footer}</Box>
            </ThemeProvider>
          ) : undefined
        }
        instanceId={waitlistRootID(kind)}
        files={files}
        folderChain={[
          { id: waitlistRootID(kind), name: `${label} waitlist`, isDir: true, droppable: !busy, draggable: false, openable: true },
          ...activeTrail.map((entry) => ({ id: entry.key, name: entry.name, isDir: true, droppable: false, draggable: false })),
        ]}
        onFileAction={action}
        disableDefaultFileActions
        fileActions={[
          ChonkyActions.EnableListView,
          ChonkyActions.OpenFiles,
          ChonkyActions.OpenParentFolder,
          RemoveSelection,
          ...(kind === "restore" ? [ChangeVersion] : []),
          ClearSelection,
        ]}
        defaultFileViewActionId={ChonkyActions.EnableListView.id}
        clearSelectionOnOutsideClick={false}
        disableDragAndDrop={busy}
      >
        <FileNavbar />
        <FileToolbar layout="inline" />
        {directory && browse.error && (
          <Alert severity="error" action={<Button onClick={browse.retry}>Retry</Button>}>
            {browse.error}
          </Alert>
        )}
        <FileList
          onScroll={({ currentTarget }) => {
            if (currentTarget.scrollTop + currentTarget.clientHeight >= currentTarget.scrollHeight - 100) browse.loadMore();
          }}
        />
        {directory && browse.loading && (
          <Box role="status" sx={{ px: 2, py: 1 }}>
            Loading folder…
          </Box>
        )}
        <FileContextMenu />
      </FileBrowser>
    </div>
  );
};
