import { forwardRef, useCallback, useMemo, type PropsWithChildren } from "react";
import { Box } from "@mui/material";
import {
  ChonkyActions,
  SortOrder,
  FileBrowser as ChonkyFileBrowser,
  type FileBrowserHandle,
  type FileBrowserProps,
  type FileData,
  type FileAction,
} from "@samuelncui/chonky";
import { useAppDispatch, useAppSelector } from "@/state/react";
import { defaultFileListPreferences, fileListActions, type FileListPreferences } from "@/state/file-lists";
import { theme } from "@/theme";

const browserTheme = { root: { borderRadius: 12 } };
const muiThemeOptions = { palette: { divider: theme.palette.divider } };
const viewActionIds = [ChonkyActions.EnableListView.id, ChonkyActions.EnableGridView.id];
// Chonky initializes the view through a registered action. Keep it without a menu button.
const listViewAction: FileAction = { ...ChonkyActions.EnableListView, button: undefined };
// Date is only the rendered value. Sorting retains differences smaller than a millisecond.
const dateSortAction = {
  ...ChonkyActions.SortFilesByDate,
  sortKeySelector: (file: FileData | null) => (typeof file?.modDateNs === "bigint" ? file.modDateNs : undefined),
};
const sortActions = { name: ChonkyActions.SortFilesByName, size: ChonkyActions.SortFilesBySize, date: dateSortAction };

export function useFileListPreferences(id: string, defaults = defaultFileListPreferences) {
  const preferences = useAppSelector((state) => state.fileLists[id] ?? defaults);
  const dispatch = useAppDispatch();
  const setPreferences = useCallback((preferences: FileListPreferences) => dispatch(fileListActions.changed({ id, preferences })), [dispatch, id]);
  return [preferences, setPreferences] as const;
}

type Props = PropsWithChildren<Omit<FileBrowserProps, "defaultFileViewActionId">>;

/** All application browsers use the same list view and public theme boundary. */
export const FileBrowser = forwardRef<FileBrowserHandle, Props>(
  ({ fileActions, disableDefaultFileActions, defaultSortActionId, onFileAction, ...props }, ref) => {
    const [preferences, setPreferences] = useFileListPreferences(props.instanceId ?? "files");
    const actions = useMemo(() => {
      const custom = (fileActions ?? []).filter((action) => !action.fileViewConfig);
      const enabled = (id: string) => disableDefaultFileActions !== true && !(disableDefaultFileActions || []).includes(id);
      const defaults: FileAction[] = Object.values(sortActions).filter((action) => enabled(action.id) && !custom.some((item) => item.id === action.id));
      const folders = ChonkyActions.ToggleShowFoldersFirst;
      if (enabled(folders.id) && !custom.some((item) => item.id === folders.id)) defaults.push(folders);
      return [listViewAction, ...custom, ...defaults].map((action) => {
        if (action.sortKeySelector && action.id === sortActions[preferences.sort].id)
          return { ...action, initialSortOrder: preferences.descending ? SortOrder.DESC : SortOrder.ASC };
        if (action.id === folders.id) return { ...action, option: { ...folders.option, defaultValue: preferences.foldersFirst } };
        return action;
      });
    }, [fileActions, disableDefaultFileActions, preferences]);
    // Loading removes sort actions. Reapply the saved choice when they return.
    const sortId = defaultSortActionId === null ? null : sortActions[preferences.sort].id;
    const activeSortId = actions.some((action) => action.id === sortId && action.sortKeySelector) ? sortId : null;
    const handleAction: NonNullable<FileBrowserProps["onFileAction"]> = (data) => {
      const action = actions.find((action) => action.id === data.id);
      const key = (Object.keys(sortActions) as Array<keyof typeof sortActions>).find((key) => sortActions[key].id === data.id);
      if (key && action?.sortKeySelector) {
        setPreferences({ ...preferences, sort: key, descending: preferences.sort === key ? !preferences.descending : key !== "name" });
      } else if (data.id === ChonkyActions.ToggleShowFoldersFirst.id) {
        setPreferences({ ...preferences, foldersFirst: !preferences.foldersFirst });
      }
      return onFileAction?.(data);
    };
    const disabledActions = useMemo(
      () => (disableDefaultFileActions === true ? true : [...viewActionIds, ...(disableDefaultFileActions || [])]),
      [disableDefaultFileActions],
    );
    return (
      <Box
        className="file-browser"
        sx={{ display: "flex", flexDirection: "column", minWidth: 0, minHeight: 0, width: "100%", height: "100%", overflow: "hidden" }}
      >
        <ChonkyFileBrowser
          theme={browserTheme}
          muiThemeOptions={muiThemeOptions}
          {...props}
          ref={ref}
          defaultSortActionId={activeSortId}
          onFileAction={handleAction}
          fileActions={actions}
          disableDefaultFileActions={disabledActions}
          defaultFileViewActionId={ChonkyActions.EnableListView.id}
        />
      </Box>
    );
  },
);
FileBrowser.displayName = "FileBrowser";
