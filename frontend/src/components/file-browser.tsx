import { forwardRef, useMemo, type PropsWithChildren } from "react";
import { Box } from "@mui/material";
import { ChonkyActions, FileBrowser as ChonkyFileBrowser, type FileBrowserHandle, type FileBrowserProps, type FileData } from "@samuelncui/chonky";
import { theme } from "@/theme";

const browserTheme = { root: { borderRadius: 12 } };
const muiThemeOptions = { palette: { divider: theme.palette.divider } };
const viewActionIds = [ChonkyActions.EnableListView.id, ChonkyActions.EnableGridView.id];
// Chonky initializes the view through a registered action. Keep it without a menu button.
const listViewAction = { ...ChonkyActions.EnableListView, button: undefined };
// Date is only the rendered value. Sorting retains differences smaller than a millisecond.
const dateSortAction = {
  ...ChonkyActions.SortFilesByDate,
  sortKeySelector: (file: FileData | null) => (typeof file?.modDateNs === "bigint" ? file.modDateNs : undefined),
};
type Props = PropsWithChildren<Omit<FileBrowserProps, "defaultFileViewActionId">>;

/** All application browsers use the same list view and public theme boundary. */
export const FileBrowser = forwardRef<FileBrowserHandle, Props>(({ fileActions, disableDefaultFileActions, ...props }, ref) => {
  const actions = useMemo(() => {
    const custom = (fileActions ?? []).filter((action) => !action.fileViewConfig);
    const dateEnabled = disableDefaultFileActions !== true && !(disableDefaultFileActions || []).includes(dateSortAction.id);
    return [listViewAction, ...custom, ...(dateEnabled ? [dateSortAction] : [])];
  }, [fileActions, disableDefaultFileActions]);
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
        fileActions={actions}
        disableDefaultFileActions={disabledActions}
        defaultFileViewActionId={ChonkyActions.EnableListView.id}
      />
    </Box>
  );
});
FileBrowser.displayName = "FileBrowser";
