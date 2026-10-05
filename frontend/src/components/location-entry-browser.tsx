import { FileBrowser } from "@/components/file-browser";
import { useCallback, useEffect, useRef, useState } from "react";
import { Box } from "@mui/material";
import {
  ChonkyActions,
  FileContextMenu,
  FileList,
  FileNavbar,
  FileToolbar,
  defineFileAction,
  type FileData,
  type ChonkyFileActionData,
} from "@samuelncui/chonky";
import { FileScope, Location, type LocationEntryRef } from "@/entity";
import { LoadMoreAction, ViewFileDetailsAction, RefreshListAction } from "@/actions";
import { DirectoryReadError } from "@/components/directory-read-error";
import { ListPlaceholder } from "@/components/list-placeholder";
import { chonkyI18n, errorMessage } from "@/tools";
import { fileLocationReference } from "@/components/location-files";
import { filesPage, locationDirectoryReference } from "@/components/files-browser";
import { fileOperationReference } from "@/components/file-operations";
import { DetailModal } from "@/pages/file-detail";

const ChooseFileAction = defineFileAction({
  id: "choose-location-file",
  requiresSelection: true,
  fileFilter: (file) => file?.isRegularFile === true,
  button: { name: "Choose file", toolbar: true, contextMenu: true },
});
const incompleteListActions = [
  ChonkyActions.SortFilesByName.id,
  ChonkyActions.SortFilesBySize.id,
  ChonkyActions.SortFilesByDate.id,
  ChonkyActions.ToggleShowFoldersFirst.id,
  ChonkyActions.SelectAllFiles.id,
];

export const LocationEntryBrowser = ({ source, onChoose, fill }: { source: Location; onChoose?: (reference: LocationEntryRef) => void; fill?: boolean }) => {
  const [directory, setDirectory] = useState({ sourceID: source.id, path: "" });
  const parent = directory.sourceID === source.id ? directory.path : "";
  const [positions, setPositions] = useState<FileData[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(true);
  const [revision, setRevision] = useState(0);
  const request = useRef(0);
  const loading = useRef(false);
  const [detail, setDetail] = useState<FileData>();
  const load = useCallback(
    async (cursor = "") => {
      const sequence = ++request.current;
      loading.current = true;
      setPending(true);
      try {
        const reply = await filesPage(locationDirectoryReference(String(source.id), parent.replace(/\/$/, "")), FileScope.ALL, cursor);
        if (sequence !== request.current) return;
        setPositions((current) => (cursor ? [...current, ...reply.files] : reply.files));
        setHasMore(!!reply.nextCursor);
        setCursor(reply.nextCursor);
        setError("");
        setRevision((value) => value + 1);
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
    setDetail(undefined);
    void load();
    return () => {
      pending.current++;
    };
  }, [load]);
  const files = positions;
  const chain: FileData[] = [{ id: `${source.id}:`, physicalPath: "", name: source.name, isDir: true }];
  let path = "";
  for (const name of parent.split("/").filter(Boolean)) {
    path += name + "/";
    chain.push({ id: `${source.id}:${path}`, physicalPath: path, name, isDir: true });
  }
  const action = (data: ChonkyFileActionData) => {
    const showDetails = (file: FileData) => {
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
      setDirectory({ sourceID: source.id, path: String(file.physicalPath ?? "") });
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
      <Box sx={fill ? { flex: 1, minHeight: 0 } : { height: 440, minHeight: 260 }}>
        <FileBrowser
          instanceId={`location:${source.id}`}
          files={files}
          folderChain={chain}
          onFileAction={action}
          disableDragAndDrop
          hideToolbarInfo={!!error || (pending && !files.length)}
          disableDefaultFileActions={hasMore || pending || !!error ? incompleteListActions : undefined}
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
          <FileList
            loading={pending && files.length ? "refreshing" : undefined}
            emptyPlaceholder={
              error ? (
                <DirectoryReadError error={error} onRetry={load} />
              ) : pending ? (
                <ListPlaceholder loading label="Reading this folder…" />
              ) : (
                <ListPlaceholder label="This folder is empty" />
              )
            }
          />
          <FileContextMenu />
        </FileBrowser>
      </Box>
      <DetailModal
        target={detail ? fileOperationReference(detail) : undefined}
        name={detail?.name}
        refreshKey={revision}
        onClose={() => setDetail(undefined)}
        onRefresh={load}
      />
    </>
  );
};
