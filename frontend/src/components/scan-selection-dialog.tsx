import { t } from "@lingui/core/macro";
import { FileBrowser } from "@/components/file-browser";
import { useCallback, useRef, useState } from "react";
import { Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, IconButton, List, ListItem, ListItemText, Typography } from "@mui/material";
import Close from "@mui/icons-material/Close";
import {
  ChonkyActions,
  FileList,
  FileNavbar,
  FileToolbar,
  defineFileAction,
  type ChonkyFileActionData,
  type FileBrowserHandle,
  type FileData,
} from "@samuelncui/chonky";
import { FileScope, FileOperationKind, type Location } from "@/entity";
import { allowsFileOperation } from "./files-browser";
import { RefreshListAction } from "@/actions";
import { useFileBrowser } from "@/components/use-file-browser";
import { librarySource, type PaneSource } from "./pane-source";
import { scanSelectionEntry, scanSelectionKey, type ScanSelectionEntry } from "./scan-selection";

const selectedPageSize = 50;
const AddSelectionAction = defineFileAction({
  id: "add-file-selection",
  requiresSelection: true,
  button: { name: "Add selected", toolbar: true },
});
const noop = () => {};
const refreshNothing = async () => {};

const accepts = (file: FileData | null | undefined) => allowsFileOperation(file, FileOperationKind.SCAN);

const removeLabel = (path: string) => t`Remove ${path}`;

export const ScanSelectionDialog = ({
  entries,
  location,
  onChoose,
  onClose,
}: {
  entries: ScanSelectionEntry[];
  location?: Location;
  onChoose: (entries: ScanSelectionEntry[]) => void;
  onClose: () => void;
}) => {
  const [draft, setDraft] = useState(() => [...entries]);
  const [visibleRoots, setVisibleRoots] = useState(selectedPageSize);
  const [source, setSource] = useState<PaneSource>(() => (location ? { kind: "location", id: String(location.id), name: location.name } : librarySource));
  const browserRef = useRef<FileBrowserHandle>(null);
  const changeSource = useCallback((value: PaneSource) => setSource(value), []);
  const browser = useFileBrowser(browserRef, "scan-selection", refreshNothing, undefined, noop, undefined, undefined, FileScope.DEFAULT, undefined, {
    source,
    onChange: changeSource,
  });
  const draftCount = draft.length;
  const chain = browser.browserProps.folderChain;
  const current = chain.at(-1);
  const canAddDirectory = accepts(current) && !(source.kind === "library" && current?.id === "0");
  const sourceLabel = source.kind === "library" ? t`Library` : source.name;
  const directory = chain
    .slice(1)
    .map((file) => file?.name)
    .filter(Boolean)
    .join("/");
  // `listProps.loading` no longer covers the initial state: a read in flight is what blocks a
  // selection, whether or not the list already has rows.
  const unavailable = !!browser.loadError || browser.listProps.reading;

  const add = (files: FileData[], parent = directory) => {
    if (unavailable || files.some((file) => !accepts(file))) return;
    const combined = new Map(draft.map((entry) => [scanSelectionKey(entry), entry]));
    for (const file of files) {
      const entry = scanSelectionEntry(file, browser.scope, sourceLabel, parent);
      combined.set(scanSelectionKey(entry), entry);
    }
    setDraft([...combined.values()]);
  };
  const addDirectory = () => {
    if (!current || !canAddDirectory || unavailable) return;
    if (source.kind === "library") {
      add(
        [current],
        chain
          .slice(1, -1)
          .map((file) => file?.name)
          .filter(Boolean)
          .join("/"),
      );
      return;
    }
    add([current]);
  };
  const onFileAction = (data: ChonkyFileActionData) => {
    if (data.id === AddSelectionAction.id) {
      add(data.state.selectedFilesForAction);
      return;
    }
    if (data.id === RefreshListAction.id) {
      browser.browserProps.onFileAction(data);
      return;
    }
    if (data.id !== ChonkyActions.OpenFiles.id) return;
    const file = data.payload.targetFile ?? data.payload.files[0];
    if (file?.isDir) {
      browser.browserProps.onFileAction(data);
    }
  };
  const close = () => {
    onClose();
  };
  return (
    <Dialog open fullWidth maxWidth="md" onClose={close} aria-labelledby="scan-selection-title">
      <DialogTitle id="scan-selection-title">{t`Choose files and folders`}</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 1, minHeight: 0 }}>
        <Box sx={{ height: 340, minHeight: 220, flexShrink: 0 }}>
          <FileBrowser
            {...browser.browserProps}
            ref={browserRef}
            instanceId="scan-selection"
            disableDragAndDrop
            clearSelectionOnOutsideClick={false}
            fileActions={[ChonkyActions.ToggleHiddenFiles, RefreshListAction, { ...AddSelectionAction, fileFilter: (file) => !unavailable && accepts(file) }]}
            onFileAction={onFileAction}
          >
            <FileNavbar rootContent={location ? undefined : browser.selector} />
            <FileToolbar layout="inline" {...browser.toolbarProps} />
            <FileList {...browser.listProps} />
          </FileBrowser>
        </Box>
        <Box sx={{ display: "flex", alignItems: "center", flexWrap: "wrap", gap: 0.5 }}>
          <Typography variant="body2" sx={{ flexGrow: 1 }}>
            {draftCount === 1 ? t`${draftCount} selected root` : t`${draftCount} selected roots`}
          </Typography>
          <Button size="small" sx={{ textTransform: "none" }} disabled={unavailable || !canAddDirectory} onClick={() => void addDirectory()}>
            {t`Add this folder`}
          </Button>
          <Button
            size="small"
            sx={{ textTransform: "none" }}
            disabled={!draft.length}
            onClick={() => {
              setDraft([]);
              setVisibleRoots(selectedPageSize);
            }}
          >
            {t`Clear`}
          </Button>
        </Box>
        {!!draft.length && (
          <List dense aria-label={t`Selected roots`} sx={{ maxHeight: 160, overflowY: "auto", flexShrink: 0, py: 0 }}>
            {draft.slice(0, visibleRoots).map((entry) => (
              <ListItem
                key={scanSelectionKey(entry)}
                disableGutters
                secondaryAction={
                  <IconButton
                    size="small"
                    aria-label={removeLabel(entry.path)}
                    onClick={() => setDraft((current) => current.filter((item) => scanSelectionKey(item) !== scanSelectionKey(entry)))}
                  >
                    <Close fontSize="small" />
                  </IconButton>
                }
              >
                <ListItemText primary={entry.path} slotProps={{ primary: { variant: "body2", noWrap: true, title: entry.path } }} />
              </ListItem>
            ))}
            {visibleRoots < draft.length && (
              <Button size="small" onClick={() => setVisibleRoots((value) => value + selectedPageSize)}>
                {t`Show more selected roots`}
              </Button>
            )}
          </List>
        )}
      </DialogContent>
      <DialogActions>
        <Button variant="contained" onClick={() => onChoose(draft)}>
          {t`Choose`}
        </Button>
        <Button onClick={close}>{t`Cancel`}</Button>
      </DialogActions>
    </Dialog>
  );
};
