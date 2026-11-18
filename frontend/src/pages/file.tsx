import { t } from "@lingui/core/macro";
import { FileBrowser as ChonkyFileBrowser } from "@/components/file-browser";
import { writeStored, stringCodec } from "@/state/storage";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams, useLocation } from "react-router";
import { toast } from "react-toastify";

import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Grid from "@mui/material/Grid";
import { FileContextMenu, FileList, FileNavbar, FileToolbar, type FileBrowserHandle, type FileData } from "@samuelncui/chonky";

import { type LibraryFileData } from "@/api";
import { FileScope, SettingsGroup } from "@/entity";
import { sourceKey, storedPaneSource, librarySource, type PaneSource } from "@/components/pane-source";
import { useCommittedSettings } from "@/components/settings-editor";

import { fileOperationReference, useFileOperations } from "@/components/file-operations";

import { FileMetadataDialog } from "@/components/file-metadata-dialog";
import { LibraryFilters } from "@/components/library-filters";
import { ToobarInfo } from "@/components/toolbarInfo";
import { DetailModal, FileInspector } from "@/components/file-detail";
import { libraryLayouts, type LibraryLayout } from "@/pages/routes";
import { ViewFileDetailsAction } from "@/actions";
import { runUIAction } from "@/tools";

import { useFileBrowser } from "@/components/use-file-browser";

const SearchStatus = ({ query, onClose }: { query: string; onClose: () => void }) => (
  <div className="search-result-bar" title={query}>
    <Button size="small" onClick={onClose}>
      {t`Back to folder`}
    </Button>
  </div>
);

export const FileBrowser = ({ layout }: { layout: LibraryLayout }) => {
  const { settings, error: settingsError } = useCommittedSettings(SettingsGroup.GENERAL);
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
  const leftRefresh = useRef<(() => Promise<void>) | null>(null);
  const rightRefresh = useRef<(() => Promise<void>) | null>(null);

  const refreshAll = useCallback(async () => {
    const refreshes = [leftRefresh.current];
    if (layout === libraryLayouts.dual) refreshes.push(rightRefresh.current);
    const results = await Promise.allSettled(refreshes.filter((refresh) => refresh !== null).map((refresh) => refresh()));
    const failure = results.find((result): result is PromiseRejectedResult => result.status === "rejected");
    if (failure) throw failure.reason;
  }, [layout]);

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
      if (locate) runUIAction(() => locate(file), t`Locate file failed`);
    },
    [layout],
  );
  const locateFromRight = useCallback((file: LibraryFileData) => {
    const locate = leftLocate.current;
    if (locate) runUIAction(() => locate(file), t`Locate file failed`);
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
  // The Inspector already details the selected file, so only Dual pane offers View Details.
  const leftActions = leftBrowser.browserProps.fileActions;
  const leftFileActions = useMemo(
    () => (layout === libraryLayouts.dual ? leftActions : leftActions.filter((action) => action.id !== ViewFileDetailsAction.id)),
    [layout, leftActions],
  );

  return (
    <Box className="browser-box library-file-browser">
      <LibraryFilters
        initialQuery={initialQuery}
        scopeLabel={source.kind === "library" ? t`Library` : source.name}
        locationScope={source.kind === "location" ? source : undefined}
        searchActive={!!leftBrowser.searchState || (layout === libraryLayouts.dual && !!rightBrowser.searchState)}
        onSearch={(query) => {
          runUIAction(() => activeBrowser.search(query), t`Search failed`);
        }}
        onClear={() =>
          runUIAction(
            async () => {
              await Promise.all([leftBrowser.closeSearch(), ...(layout === libraryLayouts.dual ? [rightBrowser.closeSearch()] : [])]);
            },
            t`Clear search failed`,
          )
        }
      />
      <Grid className="browser-container" container columnSpacing={1.5}>
        <Grid
          component="section"
          aria-label={t`Left file pane`}
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
            fileActions={leftFileActions}
            clearSelectionOnOutsideClick={clearSelectionOnOutsideClick}
          >
            {leftBrowser.searchState && (
              <SearchStatus query={leftBrowser.searchState.query} onClose={() => runUIAction(leftBrowser.closeSearch, t`Close search failed`)} />
            )}
            <FileNavbar rootContent={leftBrowser.selector} />
            <FileToolbar layout="inline" {...leftBrowser.toolbarProps}>
              {!leftBrowser.browserProps.hideToolbarInfo && <ToobarInfo files={leftBrowser.files} measurement={leftBrowser.measurement} />}
            </FileToolbar>
            {leftBrowser.errorNotice}
            <FileList {...leftBrowser.listProps} />
            <FileContextMenu />
          </ChonkyFileBrowser>
        </Grid>
        {layout === libraryLayouts.dual ? (
          <Grid
            component="section"
            aria-label={t`Right file pane`}
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
                <SearchStatus query={rightBrowser.searchState.query} onClose={() => runUIAction(rightBrowser.closeSearch, t`Close search failed`)} />
              )}
              <FileNavbar rootContent={rightBrowser.selector} />
              <FileToolbar layout="inline" {...rightBrowser.toolbarProps}>
                {!rightBrowser.browserProps.hideToolbarInfo && <ToobarInfo files={rightBrowser.files} measurement={rightBrowser.measurement} />}
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
