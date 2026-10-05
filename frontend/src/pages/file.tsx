import { FileBrowser as ChonkyFileBrowser } from "@/components/file-browser";
import { readStored, writeStored, stringCodec } from "@/state/storage";
import { useSelectionActions } from "@/state/react";
import { useCallback, useEffect, useEffectEvent, useMemo, useRef, useState, type RefObject, type UIEvent } from "react";
import { useSearchParams, useNavigate, useLocation } from "react-router";
import { toast } from "react-toastify";

import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Grid from "@mui/material/Grid";
import {
  ChonkyActions,
  FileContextMenu,
  FileList,
  FileNavbar,
  FileToolbar,
  type CustomVisibilityState,
  type ChonkyFileActionData,
  type FileArray,
  type FileBrowserHandle,
  type FileData,
} from "@samuelncui/chonky";

import { filesCli, locationCli, Root, type LibraryFileData } from "@/api";
import { FileOperationKind, FileOperationRef, FileOperationSpec, FileScope, MeasureFilesRequest, SettingsGroup } from "@/entity";
import { PaneSourceSelector, sourceKey, storedPaneSource, librarySource, type PaneSource } from "@/components/pane-source";
import { useCommittedSettings } from "@/components/settings-editor";
import { locationFilePage, locationBreadcrumbs, locationDirectoryID, locationPath, associatedLibraryFileID } from "@/components/location-files";
import {
  allowsFileOperation,
  filesEntryData,
  filesPage,
  libraryDirectoryReference,
  listDirectory,
  locationDirectoryReference,
} from "@/components/files-browser";
import type { LocationNavigation } from "@/components/original-location-link";
import { DirectoryReadError } from "@/components/directory-read-error";
import { scanSelectionEntry } from "@/components/scan-selection";
import { collectionOptions, type ScanPrefill } from "@/pages/scan-input";
import { canPasteFiles, fileOperationReference, useFileOperations, type FileOperations } from "@/components/file-operations";
import {
  AddLocationFileAction,
  ScanFilesAction,
  ArchiveLibraryAction,
  RestoreLibraryAction,
  CutFilesAction,
  PasteFilesAction,
  CreateFolder,
  EditFileMetadataAction,
  LocateInOtherPaneAction,
  RefreshListAction,
  RenameFileAction,
  ViewFileDetailsAction,
  GetDataUsageAction,
} from "@/actions";
import { selectionAddMessage, selectionEntriesForFiles, type SelectionKind } from "@/components/selection-waitlist-state";
import { FileMetadataDialog } from "@/components/file-metadata-dialog";
import { useActionDialog } from "@/components/action-dialog";
import { LibraryFilters } from "@/components/library-filters";
import { ListPlaceholder } from "@/components/list-placeholder";
import { ToobarInfo } from "@/components/toolbarInfo";
import { useFilesMeasure } from "@/components/use-files-measure";
import { DetailModal, FileInspector } from "@/pages/file-detail";
import { libraryLayouts, type LibraryLayout } from "@/pages/routes";
import { chonkyI18n, errorMessage, runUIAction } from "@/tools";

type SearchState = {
  query: string;
  originID: string;
  nextCursor: string;
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

const incompleteFileActions = [
  ChonkyActions.SortFilesByName.id,
  ChonkyActions.SortFilesBySize.id,
  ChonkyActions.SortFilesByDate.id,
  ChonkyActions.ToggleShowFoldersFirst.id,
  ChonkyActions.SelectAllFiles.id,
];

export const useFileBrowser = (
  browserRef: RefObject<FileBrowserHandle | null>,
  storageKey: string,
  refreshAll: () => Promise<void>,
  openFile: ((file: FileData) => void) | undefined,
  locateOther: (file: LibraryFileData) => void,
  onSelectionChange?: (file: FileData | null) => void,
  initial?: { query: string; fileID: string; location?: LocationNavigation },
  scope: FileScope = FileScope.DEFAULT,
  organization?: { operations: FileOperations; confirmRemove: boolean },
  sourceControl?: { source: PaneSource; onChange: (source: PaneSource) => void },
  enabled = true,
) => {
  const { add: addSelectionEntries } = useSelectionActions();
  const [localSource, setLocalSource] = useState<PaneSource>(() => (initial?.fileID ? librarySource : storedPaneSource(storageKey)));
  const source = sourceControl?.source ?? localSource;
  const changeControlledSource = sourceControl?.onChange;
  const setSource = useCallback(
    (value: PaneSource) => {
      if (changeControlledSource) {
        changeControlledSource(value);
        return;
      }
      writeStored("local", `${storageKey}:source`, JSON.stringify(value), stringCodec);
      setLocalSource(value);
    },
    [changeControlledSource, storageKey],
  );
  const [nextCursor, setNextCursor] = useState("");
  const [listingTotal, setListingTotal] = useState<bigint | undefined>(undefined);
  const [loadError, setLoadError] = useState("");
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
  const [files, setFiles] = useState<FileArray>([]);
  const measurement = useFilesMeasure(files);
  const invalidateMeasurement = measurement.invalidate;
  const [panelLoading, setPanelLoading] = useState<"initial" | "refreshing" | "more" | undefined>("initial");
  const [detailRevision, setDetailRevision] = useState(0);
  const [folderChain, setFolderChain] = useState<FileArray>([Root]);
  const [searchState, setSearchState] = useState<SearchState | null>(null);
  const [metadataFiles, setMetadataFiles] = useState<FileData[]>([]);
  const [liveDetails, setLiveDetails] = useState<FileData>();
  const { ask, dialog } = useActionDialog();
  const localOperations = useFileOperations(refreshAll);
  const operations = organization?.operations ?? localOperations;
  const request = useRef(0);
  const directoryRead = useRef<AbortController | undefined>(undefined);
  const locateRequest = useRef(0);
  const loading = useRef(false);
  const loadedPages = useRef(1);
  const loadedView = useRef("");
  const pendingReveal = useRef<string | null>(null);
  // The last listing read completely, kept so a failed refresh can fall back to it instead
  // of leaving the rows a partly read stream already published.
  const lastListing = useRef<{ files: FileArray; total?: bigint } | undefined>(undefined);
  const previousSource = useRef(source);
  const openInitialFile = useEffectEvent((file: FileData) => openFile?.(file));
  const resetSourceView = useEffectEvent(() => {
    if (previousSource.current !== source) consumedLocationRequest.current = initialLocationRequest;
    previousSource.current = source;
    request.current++;
    directoryRead.current?.abort();
    loadedView.current = "";
    setFiles([]);
    setPanelLoading("initial");
    setFolderChain([root]);
    setLoadError("");
    setLiveDetails(undefined);
    setSearchState(null);
    setNextCursor("");
    setListingTotal(undefined);
    onSelectionChange?.(null);
    // A source selected in the other pane also supersedes a pending File link.
    if (source.kind === "location") consumedFileRequest.current = initialFileRequest;
  });
  useEffect(() => resetSourceView(), [source]);

  const currentID = useMemo(() => folderChain.at(-1)?.id ?? Root.id, [folderChain]);
  useEffect(() => invalidateMeasurement(), [source, scope, enabled, invalidateMeasurement]);

  const openFolder = useCallback(
    async (id: string, cursor = "", _pageCount = 1, entered?: FileData) => {
      if (!cursor) invalidateMeasurement();
      const sequence = ++request.current;
      directoryRead.current?.abort();
      const controller = new AbortController();
      directoryRead.current = controller;
      let frame: number | undefined;
      let publishedCount = 0;
      const cancelFrame = () => {
        if (frame !== undefined) cancelAnimationFrame(frame);
        frame = undefined;
      };
      controller.signal.addEventListener("abort", cancelFrame, { once: true });
      loading.current = true;
      const view = JSON.stringify([sourceKey(source), id]);
      const navigation = loadedView.current !== view;
      if (navigation) {
        loadedView.current = "";
        setFiles([]);
        // Publish the entered directory with the loading list: a Location path is known from its ID,
        // and an opened Library folder from the row or breadcrumb entry that opened it.
        setFolderChain((current) => {
          if (source.kind === "location") return locationBreadcrumbs({ id: BigInt(source.id), name: source.name }, locationPath(id));
          const index = current.findIndex((folder) => folder?.id === id);
          if (index >= 0) return current.slice(0, index + 1);
          return entered ? [...current, entered] : current;
        });
      }
      setPanelLoading(cursor ? "more" : navigation ? "initial" : "refreshing");
      try {
        const physicalPath = source.kind === "location" ? locationPath(id) : "";
        const directory = source.kind === "library" ? libraryDirectoryReference(id) : locationDirectoryReference(source.id, physicalPath);
        // One read returns the whole directory, so a listing never continues through a
        // cursor: there is no second page to fetch. Publish the first batch immediately,
        // then coalesce snapshots at doubling sizes into frames. Chonky scans each supplied
        // array, so frame throttling alone would still copy/scan quadratic prefixes on a
        // slow stream. Doubling bounds all intermediate snapshots to fewer than 2N rows.
        const page = await listDirectory(
          directory,
          scope,
          (batch, received) => {
            if (sequence !== request.current) return;
            if (batch.first) {
              setEffectiveScope(batch.scope);
              setListingTotal(batch.total);
              if (batch.breadcrumbs.length) setFolderChain(batch.breadcrumbs.map(filesEntryData));
              setLoadError("");
              setPanelLoading("refreshing");
              publishedCount = received.length;
              setFiles(received.slice());
              return;
            }
            if (frame !== undefined || received.length < Math.max(1, publishedCount * 2)) return;
            frame = requestAnimationFrame(() => {
              frame = undefined;
              if (sequence !== request.current || controller.signal.aborted) return;
              publishedCount = received.length;
              setFiles(received.slice());
            });
          },
          controller.signal,
        );
        if (sequence !== request.current) return;
        const listing = page.files;
        loadedView.current = view;
        if (pendingReveal.current && !listing.some((file) => file.id === pendingReveal.current)) pendingReveal.current = null;
        loadedPages.current = 1;
        lastListing.current = { files: listing, total: page.total };
        setLoadError("");
        setEffectiveScope(page.scope);
        setFiles(listing);
        setDetailRevision((value) => value + 1);
        setNextCursor("");
        setListingTotal(page.total);
        setFolderChain(page.breadcrumbs.length ? page.breadcrumbs.map(filesEntryData) : [root]);
        setSearchState(null);
        writeStored("local", selectedStorageKey, id, stringCodec);
      } catch (error) {
        if (sequence !== request.current) return;
        // A streamed listing publishes as it reads, so a failed read has already put part of
        // the directory on screen. Discard it: a Library refresh falls back to the listing it
        // read completely, and a new directory or a live Location shows the error alone.
        const previous = !navigation && source.kind === "library" ? lastListing.current : undefined;
        loadedView.current = previous ? view : "";
        setFiles(previous?.files ?? []);
        setListingTotal(previous?.total);
        setNextCursor("");
        setLoadError(errorMessage(error, "Could not read this directory"));
        throw error;
      } finally {
        cancelFrame();
        controller.signal.removeEventListener("abort", cancelFrame);
        if (directoryRead.current === controller) directoryRead.current = undefined;
        if (sequence === request.current) {
          loading.current = false;
          setPanelLoading(undefined);
        }
      }
    },
    [selectedStorageKey, source, scope, root, invalidateMeasurement],
  );

  const runSearch = useCallback(
    async (query: string, originID: string, cursor = "", append = false, pageCount = 1) => {
      if (!append) invalidateMeasurement();
      const sequence = ++request.current;
      directoryRead.current?.abort();
      loading.current = true;
      const view = JSON.stringify([sourceKey(source), originID, query]);
      const navigation = loadedView.current !== view;
      if (navigation) {
        loadedView.current = "";
        setFiles([]);
      }
      if (!append) {
        setSearchState({ query, originID, nextCursor: "" });
        setListingTotal(undefined);
      }
      setPanelLoading(append ? "more" : navigation ? "initial" : "refreshing");
      try {
        if (source.kind === "location") {
          const path = locationPath(originID);
          let page = await locationFilePage(source.id, path, cursor, query);
          if (sequence !== request.current) return;
          const matches = [...page.files];
          let pages = 1;
          while (pages < pageCount && page.nextCursor) {
            page = await locationFilePage(source.id, path, page.nextCursor, query);
            if (sequence !== request.current) return;
            matches.push(...page.files);
            pages++;
          }
          if (sequence !== request.current) return;
          loadedView.current = view;
          setFiles((current) => (append ? [...current, ...matches] : matches));
          if (!append) setDetailRevision((value) => value + 1);
          setLoadError("");
          setFolderChain(page.breadcrumbs.length ? page.breadcrumbs.map(filesEntryData) : [root]);
          setSearchState({ query, originID, nextCursor: page.nextCursor });
          setListingTotal(undefined);
          loadedPages.current = append ? loadedPages.current + pages : pages;
          return;
        }
        let reply = await filesPage(libraryDirectoryReference(originID), scope, cursor, query, true);
        if (sequence !== request.current) return;
        const results = [...reply.files];
        let pages = 1;
        while (pages < pageCount && reply.nextCursor) {
          reply = await filesPage(libraryDirectoryReference(originID), scope, reply.nextCursor, query, true);
          if (sequence !== request.current) return;
          results.push(...reply.files);
          pages++;
        }
        loadedPages.current = append ? loadedPages.current + pages : pages;
        loadedView.current = view;
        setFiles((current) => (append ? [...current.filter(Boolean), ...results] : results));
        if (!append) setDetailRevision((value) => value + 1);
        setLoadError("");
        setFolderChain([root, SearchRoot]);
        setSearchState({ query, originID, nextCursor: reply.nextCursor });
      } catch (error) {
        if (sequence !== request.current) return;
        if (source.kind === "location") {
          loadedView.current = "";
          setFiles([]);
        }
        setNextCursor("");
        setSearchState({ query, originID, nextCursor: "" });
        setLoadError(errorMessage(error, "Could not read this directory"));
        throw error;
      } finally {
        if (sequence === request.current) {
          loading.current = false;
          setPanelLoading(undefined);
        }
      }
    },
    [scope, source, root, invalidateMeasurement],
  );

  const search = useCallback(
    async (value: string, grouped = false) => {
      if (grouped) {
        navigate(source.kind === "location" ? `/tools/identical?source=locations&location=${source.id}` : "/tools/identical");
        return;
      }
      const query = value.trim();
      if (!query) return;
      const originID = searchState?.originID ?? currentID;
      try {
        await runSearch(query, originID);
      } catch (error) {
        toast.error(error instanceof Error ? error.message : "Search failed");
      }
    },
    [currentID, runSearch, searchState?.originID, navigate, source],
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
      const sequence = ++locateRequest.current;
      const viewRequest = request.current;
      let detail;
      try {
        detail = (await filesCli.get({ reference: fileOperationReference(file) }).response).detail;
      } catch (error) {
        if (sequence !== locateRequest.current || viewRequest !== request.current) return false;
        throw error;
      }
      if (sequence !== locateRequest.current || viewRequest !== request.current) return false;
      const parent = detail?.organization?.parent?.target;
      if (parent?.oneofKind !== "fileId") throw new Error("This file has no Library parent.");
      const parentID = String(parent.fileId);
      pendingReveal.current = file.id;
      if (source.kind !== "library") {
        writeStored("local", `${storageKey}:library`, parentID, stringCodec);
        setSource(librarySource);
        return true;
      }
      try {
        const reading = openFolder(parentID);
        const folderRequest = request.current;
        await reading;
        return sequence === locateRequest.current && folderRequest === request.current;
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
    if (!enabled) return;
    const storedID = readStored("local", selectedStorageKey, stringCodec);
    const requestedFileID = consumedFileRequest.current !== initialFileRequest ? initial?.fileID : undefined;
    const requestedLocation =
      initialLocationID && consumedLocationRequest.current !== initialLocationRequest
        ? { id: initialLocationID, path: initialLocationPath, reveal: initialLocationReveal }
        : undefined;
    let active = true;
    runUIAction(async () => {
      try {
        if (requestedLocation) {
          const reply = await locationCli.get({ id: BigInt(requestedLocation.id) }).response;
          if (!active || consumedLocationRequest.current === initialLocationRequest) return;
          if (!reply.location) throw new Error("This Location no longer exists.");
          setLiveDetails(undefined);
          const folder = locationDirectoryID(requestedLocation.path);
          pendingReveal.current = requestedLocation.reveal ? `location-file:${requestedLocation.id}:${requestedLocation.reveal}` : null;
          if (source.kind !== "location" || source.id !== requestedLocation.id) {
            consumedLocationRequest.current = initialLocationRequest;
            writeStored("local", `${storageKey}:location:${requestedLocation.id}`, folder, stringCodec);
            setSource({ kind: "location", id: requestedLocation.id, name: reply.location.name });
            return;
          }
          await openFolder(folder);
          if (active) consumedLocationRequest.current = initialLocationRequest;
          return;
        }
        if (requestedFileID) {
          const reply = (await filesCli.get({ reference: libraryDirectoryReference(requestedFileID) }).response).detail;
          if (!active || consumedFileRequest.current === initialFileRequest) return;
          if (!reply?.entry) throw new Error("This Library file no longer exists.");
          const file = {
            ...filesEntryData(reply.entry),
            parentId: String(reply.organization?.parent?.target.oneofKind === "fileId" ? reply.organization.parent.target.fileId : 0n),
            tags: reply.organization?.tags ?? [],
            note: reply.organization?.note ?? "",
            detailsAvailable: true as const,
          };
          if (!(await locate(file))) return;
          if (!active || consumedFileRequest.current === initialFileRequest) return;
          consumedFileRequest.current = initialFileRequest;
          onSelectionChange?.(file);
          openInitialFile?.(file);
          return;
        }
        await openFolder(storedID ?? root.id);
        if (active && initial?.query) await runSearch(initial.query, storedID ?? Root.id);
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
      directoryRead.current?.abort();
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
    enabled,
  ]);

  const refresh = useCallback(
    async (_background = false) => {
      invalidateMeasurement();
      if (searchState) {
        await runSearch(searchState.query, searchState.originID, "", false, loadedPages.current);
        return;
      }
      await openFolder(currentID, "", loadedPages.current);
    },
    [currentID, openFolder, runSearch, searchState, invalidateMeasurement],
  );

  const onFileAction = useCallback(
    (data: ChonkyFileActionData) => {
      if (source.kind === "location" && loadError && ![RefreshListAction.id, ChonkyActions.OpenFiles.id, ChonkyActions.ChangeSelection.id].includes(data.id))
        return;
      const selected = data.state?.selectedFilesForAction ?? [];
      const selectedAllow = (kind: FileOperationKind) => selected.length > 0 && selected.every((file) => allowsFileOperation(file, kind));
      const openScan = (collect = false) => {
        const directory = searchState
          ? ""
          : folderChain
              .slice(1)
              .map((file) => file?.name)
              .join("/");
        const scan: ScanPrefill = {
          target: {
            kind: "files",
            entries: selected.map((file) => scanSelectionEntry(file, effectiveScope, source.kind === "library" ? "Library" : source.name, directory)),
          },
          ...(collect ? { options: collectionOptions } : {}),
        };
        navigate("/scan", { state: { scan } });
      };
      const showDetails = (file: FileData) => {
        if (openFile) openFile(file);
        else setLiveDetails(file);
      };
      const destination = async (target = folderChain.at(-1)!): Promise<FileOperationRef> => {
        if (!allowsFileOperation(target, FileOperationKind.MKDIR)) throw new Error("This folder does not allow changes. Refresh this folder.");
        const reference = target.physicalLocationID
          ? locationDirectoryReference(String(target.physicalLocationID), String(target.physicalPath))
          : fileOperationReference(target);
        const detail = (await filesCli.get({ reference }).response).detail;
        if (!detail?.entry?.reference) throw new Error("Destination observation is missing. Refresh this folder.");
        if (!detail.entry.operations.includes(FileOperationKind.MKDIR)) throw new Error("This folder does not allow changes. Refresh this folder.");
        return detail.entry.reference;
      };
      switch (data.id) {
        case GetDataUsageAction.id: {
          if (measurement.state.running) {
            measurement.cancel();
            return;
          }
          if (panelLoading) return;
          const origin = searchState?.originID ?? currentID;
          const directory = source.kind === "library" ? libraryDirectoryReference(origin) : locationDirectoryReference(source.id, locationPath(origin));
          void measurement.start(
            MeasureFilesRequest.create({
              directory,
              scope: effectiveScope,
              query: searchState?.query ?? "",
              recursive: source.kind === "library" && !!searchState,
            }),
          );
          return;
        }
        case CutFilesAction.id:
          if (!selectedAllow(FileOperationKind.MOVE)) return;
          operations.setClipboard({ kind: FileOperationKind.MOVE, files: selected });
          return;
        case PasteFilesAction.id:
          if (!canPasteFiles(operations.clipboard, folderChain.at(-1))) return;
          runUIAction(async () => operations.paste(await destination()), "Paste failed");
          return;
        case AddLocationFileAction.id:
          if (!selectedAllow(FileOperationKind.ADMIT)) return;
          openScan(true);
          return;
        case ScanFilesAction.id: {
          if (!selectedAllow(FileOperationKind.SCAN)) return;
          openScan();
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
            runUIAction(() => openFolder(file.id, "", 1, file), "Open Library folder failed");
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
          if (copy || !movedFiles.length || !movedFiles.every((file) => allowsFileOperation(file, FileOperationKind.MOVE))) return;
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
          if (!selectedAllow(FileOperationKind.MOVE)) return;
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
          if (!allowsFileOperation(folderChain.at(-1), FileOperationKind.MKDIR)) return;
          ask({
            title: "New folder",
            confirmLabel: "Create",
            input: { label: "Name" },
            onConfirm: async (name) => operations.start(FileOperationSpec.create({ kind: FileOperationKind.MKDIR, destination: await destination(), name })),
          });
          return;
        }
        case ChonkyActions.DeleteFiles.id: {
          if (!selectedAllow(FileOperationKind.REMOVE)) return;
          if (source.kind === "location" && !organization) return;
          const remove = () => operations.start(FileOperationSpec.create({ kind: FileOperationKind.REMOVE, sources: selected.map(fileOperationReference) }));
          const physical = source.kind === "location";
          if (organization && !organization.confirmRemove) {
            runUIAction(remove, "Delete failed");
            return;
          }
          ask({
            title: "Delete files?",
            confirmLabel: "Delete",
            danger: true,
            children: physical ? (
              <>
                <ul>
                  {selected.map((file) => (
                    <li key={file.id}>{String(file.physicalPath)}</li>
                  ))}
                </ul>
                <p>
                  Files and folders move into this Location’s .trash folder. Original associations are removed; Library records and saved versions are kept.
                </p>
              </>
            ) : (
              <>
                <p>
                  {selected.length} {selected.length === 1 ? "item" : "items"} will move to Library Trash.
                </p>
                <p>Original files and archive copies are kept.</p>
              </>
            ),
            onConfirm: remove,
          });
          return;
        }
        case EditFileMetadataAction.id:
          if (!selectedAllow(FileOperationKind.UPDATE_METADATA)) return;
          setMetadataFiles(data.state.selectedFilesForAction);
          return;
        case ArchiveLibraryAction.id:
        case RestoreLibraryAction.id: {
          const kind: SelectionKind = data.id === ArchiveLibraryAction.id ? "archive" : "restore";
          runUIAction(
            async () => {
              const additions = await selectionEntriesForFiles(
                kind,
                data.state.selectedFilesForAction,
                kind === "archive" ? FileScope.DEFAULT : effectiveScope,
                source.kind === "location" ? source.name : "Library",
              );
              const result = addSelectionEntries(kind, additions);
              toast.success(
                <span>
                  {selectionAddMessage(kind, result)}{" "}
                  <Button size="small" color="inherit" onClick={() => navigate(`/${kind}`)}>
                    View list
                  </Button>
                </span>,
                { closeOnClick: false },
              );
            },
            `Could not add the selection to the ${kind === "archive" ? "Archive" : "Restore"} list`,
          );
          return;
        }
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
        case RefreshListAction.id:
          runUIAction(refresh, "Refresh Library failed");
          return;
      }
    },
    [
      addSelectionEntries,
      loadError,
      locateOther,
      onSelectionChange,
      openFile,
      openFolder,
      refresh,
      searchState,
      source,
      navigate,
      effectiveScope,
      initialFileRequest,
      initialLocationRequest,
      ask,
      organization,
      operations,
      folderChain,
      measurement,
      panelLoading,
      currentID,
    ],
  );

  const fileActions = useMemo(() => {
    const allows = (kind: FileOperationKind) => (file: FileData | null) => allowsFileOperation(file, kind);
    const common = [
      ChonkyActions.ToggleHiddenFiles,
      ViewFileDetailsAction,
      { ...EditFileMetadataAction, fileFilter: allows(FileOperationKind.UPDATE_METADATA) },
      {
        ...ArchiveLibraryAction,
        button: { ...ArchiveLibraryAction.button, name: "Add to Archive list", tooltip: "Add to Archive list" },
        fileFilter: allows(FileOperationKind.ARCHIVE),
      },
      {
        ...RestoreLibraryAction,
        fileFilter: (file: FileData | null) => !!file && (!!file.isDir || !!associatedLibraryFileID(file)),
      },
      { ...ScanFilesAction, fileFilter: allows(FileOperationKind.SCAN) },
      RefreshListAction,
      {
        ...GetDataUsageAction,
        button: { ...GetDataUsageAction.button, name: "Data Usage", tooltip: measurement.state.running ? "Cancel Data Usage" : "Data Usage" },
      },
    ];
    const writable = allowsFileOperation(folderChain.at(-1), FileOperationKind.MKDIR);
    const paste = {
      ...PasteFilesAction,
      // Chonky exports this enum as a type only: Default = 2, Disabled = 1.
      customVisibility: (): CustomVisibilityState => (canPasteFiles(operations.clipboard, folderChain.at(-1)) ? 2 : 1),
    };
    const remove = {
      ...ChonkyActions.DeleteFiles,
      button: { ...ChonkyActions.DeleteFiles.button, name: "Delete", tooltip: "Delete" },
      fileFilter: allows(FileOperationKind.REMOVE),
    };
    const organize = [
      ...(writable ? [CreateFolder] : []),
      paste,
      { ...RenameFileAction, fileFilter: allows(FileOperationKind.MOVE) },
      { ...CutFilesAction, fileFilter: allows(FileOperationKind.MOVE) },
      ChonkyActions.MoveFiles,
      remove,
    ];
    if (source.kind === "location" && loadError) return [RefreshListAction];
    if (source.kind === "location")
      return [...common, { ...AddLocationFileAction, fileFilter: allows(FileOperationKind.ADMIT) }, ...(organization ? organize : [])];
    if (searchState) return [LocateInOtherPaneAction, ...common, remove];
    return [...common, ...organize];
  }, [searchState, source.kind, organization, loadError, folderChain, operations.clipboard, measurement.state.running]);

  const onScroll = useCallback(
    (event: UIEvent<HTMLDivElement>) => {
      const target = event.currentTarget;
      if (target.scrollHeight - target.scrollTop - target.clientHeight >= 160 || loading.current) return;
      if (searchState?.nextCursor) {
        void loadMoreSearch();
        return;
      }
      if (!searchState && nextCursor) runUIAction(() => openFolder(currentID, nextCursor), "Load more files failed");
    },
    [loadMoreSearch, searchState, nextCursor, openFolder, currentID],
  );
  const incompleteListing = !!panelLoading || !!searchState?.nextCursor || (!!loadError && (!!searchState || !files.length));

  return {
    browserProps: {
      files: measurement.files,
      folderChain,
      onFileAction,
      fileActions,
      // Removing the sort actions skips Chonky's sort without resetting the user's choice.
      // Keep this array stable: rebuilding its action map also invalidates list selectors.
      disableDefaultFileActions: incompleteListing ? incompleteFileActions : undefined,
      hideToolbarInfo: !!loadError || panelLoading === "initial",
      disableDragAndDrop: !!loadError || Boolean(searchState) || (source.kind === "location" && !organization),
      doubleClickDelay: 300,
      i18n: chonkyI18n,
    },
    listProps: {
      onScroll,
      // The initial read publishes its own placeholder instead of the empty list Chonky draws for
      // it; a continuation or refresh keeps its rows and Chonky's unobtrusive indicator. `reading`
      // reports the read itself, which `loading` no longer does for the initial state.
      loading: panelLoading === "initial" ? undefined : panelLoading,
      reading: panelLoading !== undefined,
      emptyPlaceholder: loadError ? (
        <DirectoryReadError error={loadError} onRetry={refresh} />
      ) : panelLoading ? (
        <ListPlaceholder loading label="Reading…" />
      ) : searchState ? (
        <ListPlaceholder label="No matching files" />
      ) : (
        <ListPlaceholder label="This folder is empty" />
      ),
    },
    files: measurement.files,
    measurement: measurement.state,
    total: listingTotal,
    source,
    loadError,
    errorNotice: loadError && files.length > 0 ? <DirectoryReadError error={loadError} onRetry={refresh} /> : null,
    detailRevision,
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
          directoryRead.current?.abort();
          setSource(value);
        }}
      />
    ),
    search,
    closeSearch,
    searchState,
    locate,
    refresh,
    metadataFiles,
    closeMetadata: () => setMetadataFiles([]),
    dialog: (
      <>
        {dialog}
        <DetailModal
          target={liveDetails ? fileOperationReference(liveDetails) : undefined}
          name={liveDetails?.name}
          refreshKey={detailRevision}
          onRefresh={refreshAll}
          onClose={() => setLiveDetails(undefined)}
        />
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
  const { settings, error: settingsError } = useCommittedSettings(SettingsGroup.LIBRARY);
  useEffect(() => {
    if (settingsError) toast.error(settingsError);
  }, [settingsError]);
  const scope = FileScope.DEFAULT;
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
  const [modalFile, setModalFile] = useState<FileData>();
  const leftLocate = useRef<((file: LibraryFileData) => Promise<boolean>) | null>(null);
  const rightLocate = useRef<((file: LibraryFileData) => Promise<boolean>) | null>(null);
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
    (file: FileData) => {
      if (layout === libraryLayouts.inspector) return;
      setModalFile(file);
    },
    [layout],
  );

  const operations = useFileOperations(refreshAll);
  const organization = { operations, confirmRemove: settings?.confirmRemove ?? true };
  const changeSource = useCallback((value: PaneSource) => {
    writeStored("local", "file_browser:left:current_id:source", JSON.stringify(value), stringCodec);
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
    layout === libraryLayouts.dual,
  );
  const currentSelectedFile = selectedFile ? (leftBrowser.files.find((file) => file?.id === selectedFile.id) ?? null) : null;

  useEffect(() => {
    setModalFile(undefined);
  }, [route.key]);

  useEffect(() => {
    leftLocate.current = leftBrowser.locate;
    rightLocate.current = rightBrowser.locate;
    leftRefresh.current = leftBrowser.refresh;
    rightRefresh.current = rightBrowser.refresh;
  }, [leftBrowser.locate, leftBrowser.refresh, rightBrowser.locate, rightBrowser.refresh]);

  useEffect(() => {
    setModalFile(undefined);
  }, [layout, source]);

  const activeBrowserPane = activePane === "left" || layout !== libraryLayouts.dual ? "left" : "right";
  const activeBrowser = activeBrowserPane === "left" ? leftBrowser : rightBrowser;
  const clearSelectionOnOutsideClick = layout === libraryLayouts.dual;

  return (
    <Box className="browser-box library-file-browser">
      <LibraryFilters
        initialQuery={initialQuery}
        scopeLabel={source.kind === "library" ? "Library" : source.name}
        locationScope={source.kind === "location" ? source : undefined}
        searchActive={!!leftBrowser.searchState || (layout === libraryLayouts.dual && !!rightBrowser.searchState)}
        onSearch={(query, grouped) => {
          runUIAction(() => activeBrowser.search(query, grouped), "Search failed");
        }}
        onClear={() =>
          runUIAction(async () => {
            await Promise.all([leftBrowser.closeSearch(), ...(layout === libraryLayouts.dual ? [rightBrowser.closeSearch()] : [])]);
          }, "Clear search failed")
        }
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
          <ChonkyFileBrowser
            key={sourceKey(source)}
            instanceId="left"
            ref={left}
            {...leftBrowser.browserProps}
            clearSelectionOnOutsideClick={clearSelectionOnOutsideClick}
          >
            {leftBrowser.searchState && (
              <SearchStatus query={leftBrowser.searchState.query} onClose={() => runUIAction(leftBrowser.closeSearch, "Close search failed")} />
            )}
            <FileNavbar rootContent={leftBrowser.selector} />
            <FileToolbar layout="inline">
              {!leftBrowser.browserProps.hideToolbarInfo && (
                <ToobarInfo files={leftBrowser.files} measurement={leftBrowser.measurement} total={leftBrowser.total} />
              )}
            </FileToolbar>
            {leftBrowser.errorNotice}
            <FileList {...leftBrowser.listProps} />
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
            <ChonkyFileBrowser
              key={sourceKey(source)}
              instanceId="right"
              ref={right}
              {...rightBrowser.browserProps}
              clearSelectionOnOutsideClick={clearSelectionOnOutsideClick}
            >
              {rightBrowser.searchState && (
                <SearchStatus query={rightBrowser.searchState.query} onClose={() => runUIAction(rightBrowser.closeSearch, "Close search failed")} />
              )}
              <FileNavbar rootContent={rightBrowser.selector} />
              <FileToolbar layout="inline">
                {!rightBrowser.browserProps.hideToolbarInfo && (
                  <ToobarInfo files={rightBrowser.files} measurement={rightBrowser.measurement} total={rightBrowser.total} />
                )}
              </FileToolbar>
              {rightBrowser.errorNotice}
              <FileList {...rightBrowser.listProps} />
              <FileContextMenu />
            </ChonkyFileBrowser>
          </Grid>
        ) : (
          <Grid className="browser" size={5}>
            <FileInspector
              target={currentSelectedFile ? fileOperationReference(currentSelectedFile) : undefined}
              name={currentSelectedFile?.name}
              refreshKey={leftBrowser.detailRevision}
              onRefresh={refreshAll}
            />
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
      <DetailModal
        target={modalFile ? fileOperationReference(modalFile) : undefined}
        name={modalFile?.name}
        refreshKey={leftBrowser.detailRevision + rightBrowser.detailRevision}
        onRefresh={refreshAll}
        onClose={() => setModalFile(undefined)}
      />
    </Box>
  );
};
