import { useCallback, useEffect, useRef, useState } from "react";
import { Box, Button, Dialog, DialogActions, DialogContent } from "@mui/material";
import {
  ChonkyActions,
  FileBrowser,
  FileContextMenu,
  FileList,
  FileNavbar,
  FileToolbar,
  defineFileAction,
  type FileData,
  type ChonkyFileActionData,
} from "@samuelncui/chonky";
import { locationCli } from "@/api";
import { LocationEntry, Location, type LocationEntryRef } from "@/entity";
import { LoadMoreAction, ViewFileDetailsAction, RefreshListAction } from "@/actions";
import { LiveFileInspector } from "@/components/live-file-inspector";
import { DirectoryReadError } from "@/components/directory-read-error";
import { chonkyI18n, errorMessage } from "@/tools";
import { associatedLibraryFileID, fileLocationReference, locationEntryFile } from "@/components/location-files";
import { DetailModal, useFileDetail } from "@/pages/file-detail";

const ChooseFileAction = defineFileAction({
  id: "choose-location-file",
  requiresSelection: true,
  fileFilter: (file) => file?.isRegularFile === true,
  button: { name: "Choose file", toolbar: true, contextMenu: true },
});

export const LocationEntryBrowser = ({ source, onChoose }: { source: Location; onChoose?: (reference: LocationEntryRef) => void }) => {
  const [parent, setParent] = useState("");
  const [positions, setPositions] = useState<LocationEntry[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(true);
  const request = useRef(0);
  const loading = useRef(false);
  const [detail, setDetail] = useState<FileData>();
  const { detail: libraryDetail, loadDetail, clearDetail } = useFileDetail();
  const load = useCallback(
    async (cursor = "") => {
      const sequence = ++request.current;
      loading.current = true;
      setPending(true);
      try {
        const reply = await locationCli.listEntries({
          locationId: source.id,
          parentPath: parent,
          cursor,
          nameFilter: "",
          limit: 200,
        }).response;
        if (sequence !== request.current) return;
        setPositions((current) => (cursor ? [...current, ...reply.entries] : reply.entries));
        setHasMore(reply.hasMore);
        setCursor(reply.nextCursor);
        setError("");
      } catch (error) {
        if (sequence === request.current) {
          setHasMore(false);
          setPositions([]);
          setCursor("");
          setError(errorMessage(error, "Could not read this directory"));
        }
      } finally {
        if (sequence === request.current) {
          loading.current = false;
          setPending(false);
        }
      }
    },
    [source.id, parent],
  );
  useEffect(() => {
    const pending = request;
    setPositions([]);
    void load();
    return () => {
      pending.current++;
    };
  }, [load]);
  const files = positions.map((entry) => locationEntryFile(entry, source));
  const chain: FileData[] = [{ id: `${source.id}:`, physicalPath: "", name: source.name, isDir: true }];
  let path = "";
  for (const name of parent.split("/").filter(Boolean)) {
    path += name + "/";
    chain.push({ id: `${source.id}:${path}`, physicalPath: path, name, isDir: true });
  }
  const action = (data: ChonkyFileActionData) => {
    const showDetails = (file: FileData) => {
      setDetail(undefined);
      clearDetail();
      const fileID = associatedLibraryFileID(file);
      if (fileID) {
        void loadDetail(fileID);
        return;
      }
      setDetail(file);
    };
    if (data.id === ChooseFileAction.id) {
      const file = data.state.selectedFilesForAction[0];
      if (file) onChoose?.(fileLocationReference(file));
      return;
    }
    if (data.id === LoadMoreAction.id) {
      if (!loading.current && hasMore) void load(cursor);
      return;
    }
    if (data.id === ViewFileDetailsAction.id) {
      const file = data.state.selectedFilesForAction[0];
      if (file) showDetails(file);
      return;
    }
    if (data.id === RefreshListAction.id) {
      void load();
      return;
    }
    if (data.id !== ChonkyActions.OpenFiles.id) return;
    const file = data.payload.targetFile ?? data.payload.files[0];
    if (!file) return;
    if (file.isDir) {
      setParent(String(file.physicalPath ?? ""));
      return;
    }
    if (!file.isRegularFile) {
      showDetails(file);
      return;
    }
    if (onChoose) {
      onChoose(fileLocationReference(file));
      return;
    }
    showDetails(file);
  };
  return (
    <>
      <Box sx={{ height: 440, minHeight: 260 }}>
        <FileBrowser
          instanceId={`online:${source.id}`}
          files={pending && !files.length ? [null] : files}
          folderChain={chain}
          onFileAction={action}
          disableDragAndDrop
          hideToolbarInfo={!!error || (pending && !files.length)}
          defaultFileViewActionId={ChonkyActions.EnableListView.id}
          fileActions={[
            ChonkyActions.ToggleHiddenFiles,
            ViewFileDetailsAction,
            RefreshListAction,
            ...(onChoose ? [ChooseFileAction] : []),
            ...(hasMore ? [LoadMoreAction] : []),
          ]}
          i18n={chonkyI18n}
        >
          <FileNavbar />
          <FileToolbar layout="inline" />
          <FileList emptyPlaceholder={error ? <DirectoryReadError error={error} onRetry={load} /> : undefined} />
          <FileContextMenu />
        </FileBrowser>
      </Box>
      <DetailModal
        detail={libraryDetail}
        onClose={clearDetail}
        onRefresh={async () => {
          await load();
          if (libraryDetail?.file) await loadDetail(String(libraryDetail.file.id));
        }}
      />
      <Dialog open={!!detail} onClose={() => setDetail(undefined)} fullWidth maxWidth="sm" slotProps={{ paper: { "aria-label": "File properties" } }}>
        <DialogContent>{detail && <LiveFileInspector file={detail} location={source} onRefresh={load} />}</DialogContent>
        <DialogActions>
          <Button onClick={() => setDetail(undefined)}>Close</Button>
        </DialogActions>
      </Dialog>
    </>
  );
};
