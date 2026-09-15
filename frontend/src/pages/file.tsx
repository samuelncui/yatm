import { useCallback, useEffect, useEffectEvent, useMemo, useRef, useState, type RefObject, type UIEvent } from "react";
import { useSearchParams, useNavigate, useLocation } from "react-router";
import { toast } from "react-toastify";

import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Alert from "@mui/material/Alert";
import Grid from "@mui/material/Grid";
import { Dialog, DialogActions, DialogContent } from "@mui/material";
import {
  ChonkyActions,
  FileBrowser as ChonkyFileBrowser,
  FileContextMenu,
  FileList,
  FileNavbar,
  FileToolbar,
  type ChonkyFileActionData,
  type FileArray,
  type FileBrowserHandle,
  type FileData,
} from "@samuelncui/chonky";

import { cli, convertFiles, convertSearchResults, filesCli, locationCli, Root, settingsCli, type LibraryFileData } from "@/api";
import { FileOperationKind, FileOperationRef, FileOperationSpec, FileScope, type LibrarySettings, type Location } from "@/entity";
import { PaneSourceSelector, sourceKey, storedPaneSource, librarySource, type PaneSource } from "@/components/pane-source";
import {
  locationFilePage,
  locationBreadcrumbs,
  locationDirectoryID,
  locationPath,
  selectionForFile,
  admitLocationFile,
  associatedLibraryFileID,
} from "@/components/location-files";
import { filesPage, filesEntryData, inspectFilePage, libraryDirectoryReference, locationDirectoryReference } from "@/components/files-browser";
import { LiveFileInspector } from "@/components/live-file-inspector";
import type { LocationNavigation } from "@/components/original-location-link";
import { DirectoryReadError } from "@/components/directory-read-error";
import { fileOperationReference, useFileOperations, type FileOperations } from "@/components/file-operations";
import {
  AddLocationFileAction,
  ScanFilesAction,
  ArchiveLibraryAction,
  CutFilesAction,
  PasteFilesAction,
  CreateFolder,
  EditFileMetadataAction,
  GetDataUsageAction,
  LocateInOtherPaneAction,
  RefreshListAction,
  RenameFileAction,
  ViewFileDetailsAction,
} from "@/actions";
import { FileMetadataDialog } from "@/components/file-metadata-dialog";
import { useActionDialog } from "@/components/action-dialog";
import { LibraryFilters } from "@/components/library-filters";
import { DuplicateGroups } from "@/components/duplicate-groups";
import { ToobarInfo } from "@/components/toolbarInfo";
import { DetailModal, FileInspector, useFileDetail } from "@/pages/file-detail";
import { libraryLayouts, type LibraryLayout } from "@/pages/routes";
import { chonkyI18n, errorMessage, runUIAction } from "@/tools";

type SearchState = {
  query: string;
  originID: string;
  nextCursor: string;
  grouped: boolean;
};

const SearchRoot: FileData = {
  id: "library-search-results",
  name: "Search Results",
  isDir: true,
  openable: false,
  selectable: false,
  draggable: false,
  droppable: false,
};

export const useFileBrowser = (
  browserRef: RefObject<FileBrowserHandle | null>,
  storageKey: string,
  refreshAll: () => Promise<void>,
  openFile: (id: string) => void,
  locateOther: (file: LibraryFileData) => void,
  onSelectionChange?: (file: FileData | null) => void,
  initial?: { query: string; fileID: string; location?: LocationNavigation },
  scope: FileScope = FileScope.DEFAULT,
  organization?: { operations: FileOperations; confirmDelete: boolean },
  sourceControl?: { source: PaneSource; onChange: (source: PaneSource) => void },
) => {
  const [localSource, setLocalSource] = useState<PaneSource>(() => (initial?.fileID ? librarySource : storedPaneSource(storageKey)));
  const source = sourceControl?.source ?? localSource;
  const changeControlledSource = sourceControl?.onChange;
  const setSource = useCallback(
    (value: PaneSource) => {
      if (changeControlledSource) {
        changeControlledSource(value);
        return;
      }
      localStorage.setItem(`${storageKey}:source`, JSON.stringify(value));
      setLocalSource(value);
    },
    [changeControlledSource, storageKey],
  );
  const [nextCursor, setNextCursor] = useState("");
  const [sourceLocation, setSourceLocation] = useState<Location>();
  const [loadError, setLoadError] = useState("");
  const [collectionError, setCollectionError] = useState("");
  const [effectiveScope, setEffectiveScope] = useState(scope);
  const selectedStorageKey = `${storageKey}:${sourceKey(source)}`;
  const root = useMemo(
    () => (source.kind === "library" ? Root : { id: locationDirectoryID(""), name: source.name, isDir: true, draggable: false, droppable: false }),
    [source],
  );
  const navigate = useNavigate();
  const route = useLocation();
  const initialFileRequest = initial?.fileID ? JSON.stringify([route.key, initial.fileID]) : undefined;
  const consumedFileRequest = useRef<string | undefined>(undefined);
  const initialLocationID = initial?.location?.id;
  const initialLocationPath = initial?.location?.path ?? "";
  const initialLocationReveal = initial?.location?.reveal ?? "";
  const initialLocationRequest = initial?.location ? JSON.stringify([route.key, initial.location]) : undefined;
  const consumedLocationRequest = useRef<string | undefined>(undefined);
  const [files, setFiles] = useState<FileArray>(Array(1).fill(null));
  const [folderChain, setFolderChain] = useState<FileArray>([Root]);
  const [searchState, setSearchState] = useState<SearchState | null>(null);
  const [metadataFiles, setMetadataFiles] = useState<LibraryFileData[]>([]);
  const [liveDetails, setLiveDetails] = useState<FileData>();
  const { ask, dialog } = useActionDialog();
  const localOperations = useFileOperations(refreshAll);
  const operations = organization?.operations ?? localOperations;
  const autoCollect = !!organization;
  const [duplicateRefresh, setDuplicateRefresh] = useState({ sequence: 0, background: false });
  const request = useRef(0);
  const loading = useRef(false);
  const loadedPages = useRef(1);
  const loadedView = useRef("");
  const pendingReveal = useRef<string | null>(null);
  const previousSource = useRef(source);
  const openInitialFile = useEffectEvent(openFile);
  const resetSourceView = useEffectEvent(() => {
    if (previousSource.current !== source) consumedLocationRequest.current = initialLocationRequest;
    previousSource.current = source;
    request.current++;
    setFiles([null]);
    setFolderChain([root]);
    setSourceLocation(undefined);
    setLoadError("");
    setCollectionError("");
    setSearchState(null);
    setNextCursor("");
    onSelectionChange?.(null);
    // A source selected in the other pane also supersedes a pending File link.
    if (source.kind === "location") consumedFileRequest.current = initialFileRequest;
  });
  useEffect(() => resetSourceView(), [source]);

  const currentID = useMemo(() => folderChain.at(-1)?.id ?? Root.id, [folderChain]);

  // Explicit follow-ups never determine which physical entries are listed.
  const observeFiles = useCallback(
    (listing: FileData[], sequence: number) => {
      const observe = async () => {
        for (let offset = 0; offset < listing.length; offset += 100) {
          if (sequence !== request.current) return;
          const batch = listing.slice(offset, offset + 100);
          let observed: FileData[] = [];
          if (source.kind === "library") observed = await inspectFilePage(batch);
          else if (autoCollect) {
            const references = batch.filter((file) => file.isRegularFile).map(fileOperationReference);
            if (references.length) observed = (await filesCli.collect({ references, automatic: true }).response).entries.map(filesEntryData);
          }
          if (sequence !== request.current) return;
          const updated = new Map(observed.map((file) => [file.id, file]));
          setFiles((current) => current.map((file) => (file ? (updated.get(file.id) ?? file) : file)));
        }
      };
      void observe().catch((error) => console.warn("Could not refresh file associations", error));
    },
    [source.kind, autoCollect],
  );

  const openFolder = useCallback(
    async (id: string, needSize = false, cursor = "", pageCount = 1) => {
      const sequence = ++request.current;
      loading.current = true;
      const view = JSON.stringify([sourceKey(source), id]);
      if (source.kind === "location" && loadedView.current !== view) setFiles([null]);
      loadedView.current = view;
      try {
        const physicalPath = source.kind === "location" ? locationPath(id) : "";
        const directory = source.kind === "library" ? libraryDirectoryReference(id) : locationDirectoryReference(source.id, physicalPath);
        const context =
          source.kind === "library"
            ? cli
                .fileListParents({ id: BigInt(id) })
                .response.then((reply) => ({ chain: [Root, ...convertFiles(reply.parents, needSize)], location: undefined }))
            : locationCli.get({ id: BigInt(source.id), revision: 0n }).response.then((reply) => {
                if (!reply.location) throw new Error("Location no longer exists");
                return { chain: locationBreadcrumbs(reply.location, physicalPath), location: reply.location };
              });
        const initial = await Promise.all([filesPage(directory, scope, cursor, "", needSize), context]);
        let page = initial[0];
        const listing = [...page.files];
        let pages = 1;
        while (page.nextCursor && (pages < pageCount || (pendingReveal.current && !listing.some((file) => file.id === pendingReveal.current)))) {
          page = await filesPage(directory, page.scope, page.nextCursor, "", needSize);
          if (sequence !== request.current) return;
          listing.push(...page.files);
          pages++;
        }
        const parents = initial[1];
        if (sequence !== request.current) return;
        if (pendingReveal.current && !page.nextCursor && !listing.some((file) => file.id === pendingReveal.current)) pendingReveal.current = null;
        loadedPages.current = cursor ? loadedPages.current + pages : pages;
        setSourceLocation(parents.location);
        setLoadError("");
        setCollectionError("");
        setEffectiveScope(page.scope);
        setFiles((current) => (cursor ? [...current, ...listing] : listing));
        setNextCursor(page.nextCursor);
        setFolderChain(parents.chain);
        setSearchState(null);
        localStorage.setItem(selectedStorageKey, id);

        observeFiles(listing, sequence);
      } catch (error) {
        if (source.kind === "location" && sequence === request.current) {
          setFiles([]);
          setNextCursor("");
          setFolderChain(locationBreadcrumbs({ id: BigInt(source.id), name: source.name }, locationPath(id)));
          setLoadError(errorMessage(error, "Could not read this directory"));
        }
        throw error;
      } finally {
        if (sequence === request.current) loading.current = false;
      }
    },
    [selectedStorageKey, source, scope, observeFiles],
  );

  const runSearch = useCallback(
    async (query: string, originID: string, cursor = "", append = false, grouped = false, pageCount = 1) => {
      const sequence = ++request.current;
      loading.current = true;
      loadedView.current = JSON.stringify([sourceKey(source), originID, query]);
      try {
        if (grouped) {
          setFiles([]);
          setFolderChain([Root, SearchRoot]);
          setSearchState({ query, originID, nextCursor: "", grouped: true });
          setDuplicateRefresh((current) => ({ sequence: current.sequence + 1, background: false }));
          return;
        }
        if (source.kind === "location") {
          const path = locationPath(originID);
          let page = await locationFilePage(source.id, path, cursor, query);
          const matches = [...page.files];
          let pages = 1;
          while (pages < pageCount && page.nextCursor) {
            page = await locationFilePage(source.id, path, page.nextCursor, query);
            matches.push(...page.files);
            pages++;
          }
          if (sequence !== request.current) return;
          setFiles((current) => (append ? [...current, ...matches] : matches));
          observeFiles(matches, sequence);
          setSourceLocation(page.location);
          setLoadError("");
          setCollectionError(page.collectionError);
          setFolderChain(locationBreadcrumbs(page.location, path));
          setSearchState({ query, originID, nextCursor: page.nextCursor, grouped: false });
          loadedPages.current = append ? loadedPages.current + pages : pages;
          return;
        }
        const input = {
          query,
          limit: 100n,
          scope,
          locationId: 0n,
          locationRevision: 0n,
        };
        let reply = await cli.fileSearch({ ...input, cursor: cursor || undefined }).response;
        if (sequence !== request.current) return;
        const results = [...reply.results];
        let pages = 1;
        while (pages < pageCount && reply.nextCursor) {
          reply = await cli.fileSearch({ ...input, cursor: reply.nextCursor }).response;
          if (sequence !== request.current) return;
          results.push(...reply.results);
          pages++;
        }
        loadedPages.current = append ? loadedPages.current + pages : pages;
        const converted = convertSearchResults(results);
        setFiles((current) => (append ? [...current.filter(Boolean), ...converted] : converted));
        observeFiles(converted, sequence);
        setFolderChain([root, SearchRoot]);
        setSearchState({ query, originID, nextCursor: reply.nextCursor, grouped: false });
      } catch (error) {
        if (source.kind === "location" && sequence === request.current) {
          setFiles([]);
          setNextCursor("");
          setSearchState({ query, originID, nextCursor: "", grouped: false });
          setLoadError(errorMessage(error, "Could not read this directory"));
        }
        throw error;
      } finally {
        if (sequence === request.current) loading.current = false;
      }
    },
    [scope, source, root, observeFiles],
  );

  const search = useCallback(
    async (value: string, grouped = value.trim() === "has:duplicates") => {
      const query = value.trim();
      if (!query) return;
      const originID = searchState?.originID ?? currentID;
      try {
        await runSearch(query, originID, "", false, grouped);
      } catch (error) {
        toast.error(error instanceof Error ? error.message : "Search failed");
      }
    },
    [currentID, runSearch, searchState?.originID],
  );

  const loadMoreSearch = useCallback(async () => {
    if (!searchState?.nextCursor || loading.current) return;
    try {
      await runSearch(searchState.query, searchState.originID, searchState.nextCursor, true);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Load more failed");
    }
  }, [runSearch, searchState]);

  const closeSearch = useCallback(async () => {
    if (!searchState) return;
    await openFolder(searchState.originID);
  }, [openFolder, searchState]);

  const locate = useCallback(
    async (file: LibraryFileData) => {
      pendingReveal.current = file.id;
      if (source.kind !== "library") {
        localStorage.setItem(`${storageKey}:library`, file.parentId);
        setSource(librarySource);
        return;
      }
      try {
        await openFolder(file.parentId);
      } catch (error) {
        pendingReveal.current = null;
        throw error;
      }
    },
    [openFolder, source.kind, storageKey, setSource],
  );

  useEffect(() => {
    const id = pendingReveal.current;
    const file = files.find((file) => file?.id === id);
    if (!id || !file) return;
    pendingReveal.current = null;
    browserRef.current?.revealFile(id);
    onSelectionChange?.(file);
  }, [browserRef, files, onSelectionChange]);

  useEffect(() => {
    const storedID = localStorage.getItem(selectedStorageKey);
    const requestedFileID = consumedFileRequest.current !== initialFileRequest ? initial?.fileID : undefined;
    const requestedLocation =
      initialLocationID && consumedLocationRequest.current !== initialLocationRequest
        ? { id: initialLocationID, path: initialLocationPath, reveal: initialLocationReveal }
        : undefined;
    let active = true;
    runUIAction(async () => {
      try {
        if (requestedLocation) {
          const reply = await locationCli.get({ id: BigInt(requestedLocation.id), revision: 0n }).response;
          if (!active || consumedLocationRequest.current === initialLocationRequest) return;
          if (!reply.location) throw new Error("This Location no longer exists.");
          setLiveDetails(undefined);
          const folder = locationDirectoryID(requestedLocation.path);
          pendingReveal.current = requestedLocation.reveal ? `location-file:${requestedLocation.id}:${requestedLocation.reveal}` : null;
          if (source.kind !== "location" || source.id !== requestedLocation.id) {
            consumedLocationRequest.current = initialLocationRequest;
            localStorage.setItem(`${storageKey}:location:${requestedLocation.id}`, folder);
            setSource({ kind: "location", id: requestedLocation.id, name: reply.location.name });
            return;
          }
          await openFolder(folder);
          if (active) consumedLocationRequest.current = initialLocationRequest;
          return;
        }
        if (requestedFileID) {
          const reply = await cli.fileGet({ id: BigInt(requestedFileID), scope: FileScope.ALL, cursor: "", limit: 100 }).response;
          if (!active || consumedFileRequest.current === initialFileRequest) return;
          if (!reply.file) throw new Error("This Library file no longer exists.");
          const file = convertFiles([reply.file])[0];
          await locate(file);
          if (!active || consumedFileRequest.current === initialFileRequest) return;
          consumedFileRequest.current = initialFileRequest;
          onSelectionChange?.(file);
          openInitialFile(file.id);
          return;
        }
        await openFolder(storedID ?? root.id);
        if (active && initial?.query) await runSearch(initial.query, storedID ?? Root.id, "", false, initial.query === "has:duplicates");
      } catch (error) {
        if (!active) return;
        if (requestedLocation && consumedLocationRequest.current === initialLocationRequest) return;
        if (requestedLocation) throw error;
        if (requestedFileID && consumedFileRequest.current === initialFileRequest) return;
        if (requestedFileID || !storedID || source.kind === "location") throw error;
        console.error("Open stored Library directory failed", error);
        await openFolder(root.id);
      }
    }, "Open Library folder failed");
    return () => {
      active = false;
      request.current += 1;
      loading.current = false;
    };
  }, [
    openFolder,
    selectedStorageKey,
    root.id,
    initial?.fileID,
    initial?.query,
    initialFileRequest,
    initialLocationID,
    initialLocationPath,
    initialLocationReveal,
    initialLocationRequest,
    locate,
    onSelectionChange,
    runSearch,
    source,
    storageKey,
    setSource,
  ]);

  const refresh = useCallback(
    async (background = false) => {
      if (loading.current) return;
      if (searchState?.grouped) {
        setDuplicateRefresh((current) => ({ sequence: current.sequence + 1, background }));
        return;
      }
      if (searchState) {
        await runSearch(searchState.query, searchState.originID, "", false, false, loadedPages.current);
        return;
      }
      await openFolder(currentID, false, "", loadedPages.current);
    },
    [currentID, openFolder, runSearch, searchState],
  );

  const onFileAction = useCallback(
    (data: ChonkyFileActionData) => {
      if (source.kind === "location" && loadError && ![RefreshListAction.id, ChonkyActions.OpenFiles.id, ChonkyActions.ChangeSelection.id].includes(data.id))
        return;
      const selected = data.state?.selectedFilesForAction ?? [];
      const showDetails = (file: FileData) => {
        const fileID = associatedLibraryFileID(file);
        if (fileID) {
          openFile(fileID);
          return;
        }
        if (file.physicalLocationID) setLiveDetails(file);
      };
      const destination = async (target = folderChain.at(-1)!): Promise<FileOperationRef> => {
        const reference = target.physicalLocationID
          ? locationDirectoryReference(String(target.physicalLocationID), String(target.physicalPath))
          : fileOperationReference(target);
        const entry = await filesCli.get({ reference }).response;
        if (!entry?.reference) throw new Error("Destination observation is missing. Refresh this folder.");
        return entry.reference;
      };
      switch (data.id) {
        case CutFilesAction.id:
          operations.setClipboard({ kind: FileOperationKind.MOVE, files: selected });
          return;
        case PasteFilesAction.id:
          runUIAction(async () => operations.paste(await destination()), "Paste failed");
          return;
        case AddLocationFileAction.id:
          runUIAction(async () => {
            for (const file of selected) await admitLocationFile(file);
            await refreshAll();
          }, "Could not add files to Library");
          return;
        case ScanFilesAction.id: {
          navigate("/scan", { state: { selections: selected.map((file) => selectionForFile(file, effectiveScope)) } });
          return;
        }
        case ChonkyActions.OpenFiles.id: {
          const file = data.payload.targetFile ?? data.payload.files[0];
          if (!file) return;
          consumedFileRequest.current = initialFileRequest;
          consumedLocationRequest.current = initialLocationRequest;
          pendingReveal.current = null;
          if (searchState && source.kind === "library") {
            if (file.detailsAvailable === true) {
              locateOther(file as LibraryFileData);
            } else if (file.id === Root.id) {
              runUIAction(() => openFolder(Root.id), "Open Library root failed");
            }
            return;
          }
          if (file.isDir) {
            runUIAction(() => openFolder(file.id), "Open Library folder failed");
            return;
          }
          showDetails(file);
          return;
        }
        case ChonkyActions.ChangeSelection.id:
          onSelectionChange?.(data.state.selectedFiles.length === 1 ? data.state.selectedFiles[0] : null);
          return;
        case ChonkyActions.MoveFiles.id: {
          const { destination: target, files: movedFiles, copy } = data.payload;
          if (copy) return;
          runUIAction(
            async () =>
              operations.start(
                FileOperationSpec.create({
                  kind: FileOperationKind.MOVE,
                  sources: movedFiles.map(fileOperationReference),
                  destination: await destination(target),
                }),
              ),
            "Move files failed",
          );
          return;
        }
        case RenameFileAction.id: {
          if (selected.length !== 1) {
            toast.info("Select one file or folder to rename.");
            return;
          }
          const file = selected[0];
          ask({
            title: "Rename",
            confirmLabel: "Rename",
            input: { label: "Name", defaultValue: file.name },
            onConfirm: async (name) =>
              operations.start(
                FileOperationSpec.create({ kind: FileOperationKind.MOVE, sources: [fileOperationReference(file)], destination: await destination(), name }),
              ),
          });
          return;
        }
        case CreateFolder.id: {
          ask({
            title: "New folder",
            confirmLabel: "Create",
            input: { label: "Name" },
            onConfirm: async (name) =>
              operations.start(FileOperationSpec.create({ kind: FileOperationKind.MAKE_DIRECTORY, destination: await destination(), name })),
          });
          return;
        }
        case ChonkyActions.DeleteFiles.id: {
          if (!selected.length) return;
          if (source.kind === "location" && !organization) return;
          const remove = () => operations.start(FileOperationSpec.create({ kind: FileOperationKind.DELETE, sources: selected.map(fileOperationReference) }));
          const permanent = source.kind === "location";
          if (permanent && !organization?.confirmDelete) {
            runUIAction(remove, "Delete failed");
            return;
          }
          ask({
            title: permanent ? "Permanently delete from disk?" : "Delete Library items?",
            confirmLabel: permanent ? "Delete permanently" : "Delete",
            danger: true,
            children: permanent ? (
              <>
                <ul>
                  {selected.map((file) => (
                    <li key={file.id}>{String(file.physicalPath)}</li>
                  ))}
                </ul>
                <p>Folders include all their contents. This cannot be undone. Library records and saved versions are kept.</p>
              </>
            ) : (
              <>
                <p>
                  {selected.length} {selected.length === 1 ? "item" : "items"}. Items move to Trash; only empty folders already in Trash are removed.
                </p>
                <p>Original files and archive copies are kept.</p>
              </>
            ),
            onConfirm: remove,
          });
          return;
        }
        case EditFileMetadataAction.id:
          runUIAction(async () => {
            const selected: LibraryFileData[] = [];
            for (const file of data.state.selectedFilesForAction) selected.push(await admitLocationFile(file));
            setMetadataFiles(selected);
          }, "Could not prepare file annotations");
          return;
        case ArchiveLibraryAction.id:
          try {
            const selections = data.state.selectedFilesForAction.map((file) => ({
              selection: selectionForFile(file, effectiveScope),
              name: file.name,
              path: file.physicalPath !== undefined ? `${sourceLocation?.name ?? "Location"}/${file.physicalPath}` : file.name,
              fileID: associatedLibraryFileID(file),
            }));
            navigate("/backup", { state: { selections } });
          } catch (error) {
            toast.error(errorMessage(error, "Could not add files to backup"));
          }
          return;
        case LocateInOtherPaneAction.id: {
          const file = data.state.selectedFilesForAction[0] as LibraryFileData | undefined;
          if (file) locateOther(file);
          return;
        }
        case ViewFileDetailsAction.id: {
          const file = data.state.selectedFilesForAction[0];
          if (file) showDetails(file);
          return;
        }
        case GetDataUsageAction.id:
          runUIAction(() => openFolder(currentID, true), "Calculate data usage failed");
          return;
        case RefreshListAction.id:
          runUIAction(refresh, "Refresh Library failed");
          return;
      }
    },
    [
      currentID,
      loadError,
      locateOther,
      onSelectionChange,
      openFile,
      openFolder,
      refresh,
      searchState,
      source,
      sourceLocation,
      navigate,
      effectiveScope,
      initialFileRequest,
      initialLocationRequest,
      ask,
      organization,
      operations,
      folderChain,
      refreshAll,
    ],
  );

  const fileActions = useMemo(() => {
    const allows = (kind: FileOperationKind) => (file: FileData | null) =>
      !!file && (!Array.isArray(file.allowedOperations) || file.allowedOperations.includes(kind));
    const common = [ChonkyActions.ToggleHiddenFiles, ViewFileDetailsAction, EditFileMetadataAction, ArchiveLibraryAction, ScanFilesAction, RefreshListAction];
    const organize = [
      CreateFolder,
      { ...RenameFileAction, fileFilter: allows(FileOperationKind.MOVE) },
      { ...CutFilesAction, fileFilter: allows(FileOperationKind.MOVE) },
      PasteFilesAction,
      ChonkyActions.MoveFiles,
      { ...ChonkyActions.DeleteFiles, fileFilter: allows(FileOperationKind.DELETE) },
    ];
    if (source.kind === "location" && loadError) return [RefreshListAction];
    if (source.kind === "location")
      return [
        ...common.filter((action) => action.id !== EditFileMetadataAction.id && action.id !== ArchiveLibraryAction.id),
        { ...EditFileMetadataAction, fileFilter: (file: FileData | null) => file?.isRegularFile === true },
        { ...ArchiveLibraryAction, fileFilter: (file: FileData | null) => !!file && (!!file.isDir || file.isRegularFile === true) },
        AddLocationFileAction,
        ...(organization ? organize : []),
      ];
    if (searchState?.grouped)
      return [
        { ...ChonkyActions.EnableListView, fileViewConfig: { ...ChonkyActions.EnableListView.fileViewConfig, entryHeight: 80 } },
        LocateInOtherPaneAction,
        ...common,
        ChonkyActions.DeleteFiles,
      ];
    if (searchState) return [LocateInOtherPaneAction, ...common, ChonkyActions.DeleteFiles];
    return [GetDataUsageAction, ...common, ...organize];
  }, [searchState, source.kind, organization, loadError]);

  const onScroll = useCallback(
    (event: UIEvent<HTMLDivElement>) => {
      const target = event.currentTarget;
      if (target.scrollHeight - target.scrollTop - target.clientHeight >= 160 || loading.current) return;
      if (searchState?.nextCursor) {
        void loadMoreSearch();
        return;
      }
      if (!searchState && nextCursor) runUIAction(() => openFolder(currentID, false, nextCursor), "Load more files failed");
    },
    [loadMoreSearch, searchState, nextCursor, openFolder, currentID],
  );

  return {
    browserProps: {
      files,
      folderChain,
      onFileAction,
      fileActions,
      hideToolbarInfo: !!loadError || files.some((file) => !file) || (!!searchState?.grouped && files.length === 0),
      disableDragAndDrop: !!loadError || Boolean(searchState) || (source.kind === "location" && !organization),
      defaultFileViewActionId: ChonkyActions.EnableListView.id,
      doubleClickDelay: 300,
      i18n: chonkyI18n,
    },
    listProps: { onScroll, emptyPlaceholder: loadError ? <DirectoryReadError error={loadError} onRetry={refresh} /> : undefined },
    files,
    source,
    loadError,
    collectionError,
    sourceLocation: source.kind === "location" ? sourceLocation : undefined,
    scope: effectiveScope,
    selector: (
      <PaneSourceSelector
        source={source}
        onNavigateRoot={() => {
          consumedFileRequest.current = initialFileRequest;
          consumedLocationRequest.current = initialLocationRequest;
          pendingReveal.current = null;
          runUIAction(() => openFolder(root.id), "Could not open root");
        }}
        onChange={(value) => {
          if (JSON.stringify(value) === JSON.stringify(source)) return;
          consumedFileRequest.current = initialFileRequest;
          consumedLocationRequest.current = initialLocationRequest;
          pendingReveal.current = null;
          request.current++;
          setSource(value);
        }}
      />
    ),
    search,
    closeSearch,
    searchState,
    duplicateRefresh,
    setDuplicateFiles: setFiles,
    locate,
    refresh,
    metadataFiles,
    closeMetadata: () => setMetadataFiles([]),
    dialog: (
      <>
        {dialog}
        <Dialog
          open={!!liveDetails}
          onClose={() => setLiveDetails(undefined)}
          fullWidth
          maxWidth="sm"
          slotProps={{ paper: { "aria-label": "File properties" } }}
        >
          <DialogContent>{liveDetails && <LiveFileInspector file={liveDetails} location={sourceLocation} onRefresh={refreshAll} />}</DialogContent>
          <DialogActions>
            <Button onClick={() => setLiveDetails(undefined)}>Close</Button>
          </DialogActions>
        </Dialog>
      </>
    ),
  };
};

const SearchStatus = ({ query, onClose }: { query: string; onClose: () => void }) => (
  <div className="search-result-bar" title={query}>
    <Button size="small" onClick={onClose}>
      Back to folder
    </Button>
  </div>
);

export const FileBrowser = ({ layout }: { layout: LibraryLayout }) => {
  const [settings, setSettings] = useState<LibrarySettings>();
  useEffect(() => {
    runUIAction(async () => setSettings(await settingsCli.getLibrary({}).response), "Could not load Library settings");
  }, []);
  const scope = settings ? (settings.includeUnbackedFiles ? FileScope.ALL : FileScope.SAVED) : FileScope.DEFAULT;
  const [params] = useSearchParams();
  const route = useLocation();
  const initialQuery = params.get("q") ?? "";
  const initialFile = /^[1-9]\d*$/.test(params.get("file") ?? "") ? params.get("file")! : "";
  const initialLocation = useMemo(() => {
    const id = params.get("location") ?? "";
    if (!/^[1-9]\d*$/.test(id)) return undefined;
    return { id, path: params.get("path") ?? "", reveal: params.get("reveal") ?? "" };
  }, [params]);
  const [source, setSource] = useState<PaneSource>(() => (initialFile ? librarySource : storedPaneSource("file_browser:left:current_id")));
  const left = useRef<FileBrowserHandle>(null);
  const right = useRef<FileBrowserHandle>(null);
  const [activePane, setActivePane] = useState<"left" | "right">("left");
  const [selectedFile, setSelectedFile] = useState<FileData | null>(null);
  const [detailModalOpen, setDetailModalOpen] = useState(false);
  const { detail, loading, loadDetail, clearDetail } = useFileDetail();
  const leftLocate = useRef<((file: LibraryFileData) => Promise<void>) | null>(null);
  const rightLocate = useRef<((file: LibraryFileData) => Promise<void>) | null>(null);
  const leftRefresh = useRef<((background?: boolean) => Promise<void>) | null>(null);
  const rightRefresh = useRef<((background?: boolean) => Promise<void>) | null>(null);

  const refreshAll = useCallback(
    async (background = false) => {
      const refreshes = [leftRefresh.current];
      if (layout === libraryLayouts.dual) refreshes.push(rightRefresh.current);
      const results = await Promise.allSettled(
        refreshes.filter((refresh): refresh is (background?: boolean) => Promise<void> => refresh !== null).map((refresh) => refresh(background)),
      );
      const failure = results.find((result): result is PromiseRejectedResult => result.status === "rejected");
      if (failure) throw failure.reason;
    },
    [layout],
  );

  const openFile = useCallback(
    (id: string) => {
      if (layout === libraryLayouts.inspector) return;
      setDetailModalOpen(true);
      void loadDetail(id);
    },
    [layout, loadDetail],
  );

  const operations = useFileOperations(refreshAll);
  const organization = { operations, confirmDelete: settings?.confirmPermanentDelete ?? true };
  const changeSource = useCallback((value: PaneSource) => {
    localStorage.setItem("file_browser:left:current_id:source", JSON.stringify(value));
    setSource(value);
  }, []);
  const sourceControl = { source, onChange: changeSource };

  const locateFromLeft = useCallback(
    (file: LibraryFileData) => {
      const locate = layout === libraryLayouts.dual ? rightLocate.current : leftLocate.current;
      if (locate) runUIAction(() => locate(file), "Locate file failed");
    },
    [layout],
  );
  const locateFromRight = useCallback((file: LibraryFileData) => {
    const locate = leftLocate.current;
    if (locate) runUIAction(() => locate(file), "Locate file failed");
  }, []);

  const leftBrowser = useFileBrowser(
    left,
    "file_browser:left:current_id",
    refreshAll,
    openFile,
    locateFromLeft,
    setSelectedFile,
    {
      query: initialQuery,
      fileID: initialFile,
      location: initialLocation,
    },
    scope,
    organization,
    sourceControl,
  );
  const rightBrowser = useFileBrowser(
    right,
    "file_browser:right:current_id",
    refreshAll,
    openFile,
    locateFromRight,
    undefined,
    undefined,
    scope,
    organization,
    sourceControl,
  );
  const currentSelectedFile = selectedFile ? (leftBrowser.files.find((file) => file?.id === selectedFile.id) ?? null) : null;
  const selectedLibraryFileID = currentSelectedFile ? associatedLibraryFileID(currentSelectedFile) : undefined;

  useEffect(() => {
    setDetailModalOpen(false);
  }, [route.key]);

  useEffect(() => {
    leftLocate.current = leftBrowser.locate;
    rightLocate.current = rightBrowser.locate;
    leftRefresh.current = leftBrowser.refresh;
    rightRefresh.current = rightBrowser.refresh;
  }, [leftBrowser.locate, leftBrowser.refresh, rightBrowser.locate, rightBrowser.refresh]);

  useEffect(() => {
    if (layout !== libraryLayouts.inspector) return;
    setDetailModalOpen(false);
    if (!selectedLibraryFileID) {
      clearDetail();
      return;
    }
    void loadDetail(selectedLibraryFileID);
  }, [clearDetail, layout, loadDetail, selectedLibraryFileID]);

  useEffect(() => {
    let active = true;
    let timer: number;
    const schedule = () => {
      timer = window.setTimeout(async () => {
        try {
          await refreshAll(true);
        } catch (error) {
          console.error("Background Library refresh failed", error);
        }
        if (active) schedule();
      }, 10000);
    };
    schedule();
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [refreshAll]);

  useEffect(() => {
    const refresh = rightRefresh.current;
    if (layout === libraryLayouts.dual && refresh) runUIAction(refresh, "Refresh right Library pane failed");
  }, [layout]);

  const activeBrowser = activePane === "left" || layout !== libraryLayouts.dual ? leftBrowser : rightBrowser;
  const clearSelectionOnOutsideClick = layout === libraryLayouts.dual;
  const refreshDetail = useCallback(async () => {
    await refreshAll();
    if (detail?.file) await loadDetail(detail.file.id.toString());
  }, [detail, loadDetail, refreshAll]);

  return (
    <Box className="browser-box library-file-browser">
      <LibraryFilters
        initialQuery={initialQuery}
        scopeLabel={source.kind === "library" ? "Library" : source.name}
        locationScope={source.kind === "location" ? source : undefined}
        onSearch={(query, grouped) => runUIAction(() => activeBrowser.search(query, grouped), "Search failed")}
        onClear={() => runUIAction(activeBrowser.closeSearch, "Clear search failed")}
      />
      <Grid className="browser-container" container columnSpacing={1.5}>
        <Grid
          component="section"
          aria-label="Left file pane"
          className={activePane === "left" || layout !== libraryLayouts.dual ? "browser browser-active" : "browser"}
          size={layout === libraryLayouts.dual ? 6 : 7}
          onMouseDown={() => setActivePane("left")}
          onFocusCapture={() => setActivePane("left")}
        >
          {leftBrowser.collectionError && <Alert severity="warning">Could not add files to Library: {leftBrowser.collectionError}</Alert>}
          <ChonkyFileBrowser
            key={`${sourceKey(source)}:${!!leftBrowser.searchState?.grouped}`}
            instanceId="left"
            ref={left}
            {...leftBrowser.browserProps}
            clearSelectionOnOutsideClick={clearSelectionOnOutsideClick}
          >
            {leftBrowser.searchState && (
              <SearchStatus query={leftBrowser.searchState.query} onClose={() => runUIAction(leftBrowser.closeSearch, "Close search failed")} />
            )}
            <FileNavbar rootContent={leftBrowser.selector} />
            <FileToolbar layout="inline">{!leftBrowser.browserProps.hideToolbarInfo && <ToobarInfo files={leftBrowser.files} />}</FileToolbar>
            {leftBrowser.searchState?.grouped ? (
              <DuplicateGroups
                key={leftBrowser.searchState.query}
                query={leftBrowser.searchState.query}
                refresh={leftBrowser.duplicateRefresh}
                onFiles={leftBrowser.setDuplicateFiles}
              />
            ) : (
              <FileList {...leftBrowser.listProps} />
            )}
            <FileContextMenu />
          </ChonkyFileBrowser>
        </Grid>
        {layout === libraryLayouts.dual ? (
          <Grid
            component="section"
            aria-label="Right file pane"
            className={activePane === "right" ? "browser browser-active" : "browser"}
            size={6}
            onMouseDown={() => setActivePane("right")}
            onFocusCapture={() => setActivePane("right")}
          >
            {rightBrowser.collectionError && <Alert severity="warning">Could not add files to Library: {rightBrowser.collectionError}</Alert>}
            <ChonkyFileBrowser
              key={`${sourceKey(source)}:${!!rightBrowser.searchState?.grouped}`}
              instanceId="right"
              ref={right}
              {...rightBrowser.browserProps}
              clearSelectionOnOutsideClick={clearSelectionOnOutsideClick}
            >
              {rightBrowser.searchState && (
                <SearchStatus query={rightBrowser.searchState.query} onClose={() => runUIAction(rightBrowser.closeSearch, "Close search failed")} />
              )}
              <FileNavbar rootContent={rightBrowser.selector} />
              <FileToolbar layout="inline">{!rightBrowser.browserProps.hideToolbarInfo && <ToobarInfo files={rightBrowser.files} />}</FileToolbar>
              {rightBrowser.searchState?.grouped ? (
                <DuplicateGroups
                  key={rightBrowser.searchState.query}
                  query={rightBrowser.searchState.query}
                  refresh={rightBrowser.duplicateRefresh}
                  onFiles={rightBrowser.setDuplicateFiles}
                />
              ) : (
                <FileList {...rightBrowser.listProps} />
              )}
              <FileContextMenu />
            </ChonkyFileBrowser>
          </Grid>
        ) : (
          <Grid className="browser" size={5}>
            {currentSelectedFile?.physicalLocationID && !selectedLibraryFileID ? (
              <LiveFileInspector file={currentSelectedFile} location={leftBrowser.sourceLocation} onRefresh={refreshAll} />
            ) : (
              <FileInspector selected={currentSelectedFile} detail={detail} loading={loading} onRefresh={refreshDetail} />
            )}
          </Grid>
        )}
      </Grid>
      {leftBrowser.dialog}
      {rightBrowser.dialog}
      <FileMetadataDialog
        files={leftBrowser.metadataFiles}
        open={leftBrowser.metadataFiles.length > 0}
        onClose={leftBrowser.closeMetadata}
        onSaved={refreshAll}
      />
      <FileMetadataDialog
        files={rightBrowser.metadataFiles}
        open={rightBrowser.metadataFiles.length > 0}
        onClose={rightBrowser.closeMetadata}
        onSaved={refreshAll}
      />
      <DetailModal detail={detailModalOpen ? detail : null} onRefresh={refreshDetail} onClose={() => setDetailModalOpen(false)} />
    </Box>
  );
};
