import { useState, useEffect, useMemo, useCallback, FC, useRef, RefObject } from "react";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import { FileBrowser, FileNavbar, FileToolbar, FileList, FileContextMenu, FileArray, FileBrowserHandle } from "@samuelncui/chonky";
import { ChonkyActions, ChonkyFileActionData, FileData } from "@samuelncui/chonky";

import { ToobarInfo } from "../components/toolbarInfo";

import { cli, convertFiles } from "../api";
import { Root } from "../api";
import { AddFileAction, RefreshListAction, CreateRestoreJobAction } from "../actions";
import { JobCreateRequest, JobRestoreParam, Source } from "../entity";

const useRestoreSourceBrowser = (target: RefObject<FileBrowserHandle>) => {
  const [files, setFiles] = useState<FileArray>(Array(1).fill(null));
  const [folderChain, setFolderChan] = useState<FileArray>([Root]);

  const openFolder = useCallback(async (id: string) => {
    const [file, folderChain] = await Promise.all([cli.fileGet({ id: BigInt(id) }).response, cli.fileListParents({ id: BigInt(id) }).response]);

    setFiles(convertFiles(file.children).map((file) => ({ ...file, droppable: false })));
    setFolderChan([Root, ...convertFiles(folderChain.parents)]);
  }, []);
  useEffect(() => {
    openFolder(Root.id);
  }, []);

  const onFileAction = useCallback(
    (data: ChonkyFileActionData) => {
      switch (data.id) {
        case ChonkyActions.OpenFiles.id:
          (async () => {
            const { targetFile, files } = data.payload;

            const fileToOpen = targetFile ?? files[0];
            if (!fileToOpen) {
              return;
            }

            if (fileToOpen.isDir) {
              await openFolder(fileToOpen.id);
              return;
            }
          })();

          return;
        case ChonkyActions.EndDragNDrop.id:
          (async () => {
            if (!target.current) {
              return;
            }

            const base = folderChain
              .filter((file): file is FileData => !!file && file.id !== "0")
              .map((file) => file.name)
              .join("/");

            const selectedFiles = data.payload.selectedFiles.map((file) => ({
              ...file,
              name: base ? base + "/" + file.name : file.name,
              openable: false,
              draggable: false,
            }));
            await target.current.requestFileAction(AddFileAction, { ...data.payload, selectedFiles });
          })();

          return;
      }

      console.log("source done", data);
    },
    [openFolder, target, folderChain],
  );

  const fileActions = useMemo(() => [ChonkyActions.StartDragNDrop, RefreshListAction], []);

  return {
    files,
    folderChain,
    onFileAction,
    fileActions,
    defaultFileViewActionId: ChonkyActions.EnableListView.id,
    doubleClickDelay: 300,
  };
};

const useRestoreTargetBrowser = () => {
  const [files, setFiles] = useState<FileArray>(Array(0));
  const [folderChain, setFolderChan] = useState<FileArray>([
    {
      id: "restore_waitlist",
      name: "Restore Waitlist",
      isDir: true,
      openable: true,
      selectable: true,
      draggable: true,
      droppable: true,
    },
  ]);

  const onFileAction = useCallback(
    (data: ChonkyFileActionData) => {
      switch (data.id) {
        case ChonkyActions.DeleteFiles.id:
          (() => {
            const remotedIDs = new Set(data.state.selectedFiles.map((file) => file.id));
            setFiles([...files.filter((file) => file && !remotedIDs.has(file.id))]);
          })();
          return;
        case AddFileAction.id:
          setFiles([...files, ...((data.payload as any)?.selectedFiles as FileData[])]);
          return;
        case CreateRestoreJobAction.id:
          (async () => {
            const fileIds = files.filter((file): file is FileData => !!file && file.id !== "0").map((file) => BigInt(file.id));
            console.log(await cli.jobCreate(makeParam(1n, { fileIds })).response);
            alert("Create Restore Job Success!");
          })();
          return;
      }
    },
    [files, setFiles],
  );

  const fileActions = useMemo(() => [ChonkyActions.DeleteFiles, AddFileAction, CreateRestoreJobAction], []);

  return {
    files,
    folderChain,
    onFileAction,
    fileActions,
    defaultFileViewActionId: ChonkyActions.EnableListView.id,
    doubleClickDelay: 300,
  };
};

export const RestoreType = "restore";

export const RestoreBrowser = () => {
  const target = useRef<FileBrowserHandle>(null);
  const sourceProps = useRestoreSourceBrowser(target);
  const targetProps = useRestoreTargetBrowser();

  return (
    <Box className="browser-box">
      <Grid className="browser-container" container>
        <Grid className="browser" item xs={6}>
          <FileBrowser {...sourceProps}>
            <FileNavbar />
            <FileToolbar>
              <ToobarInfo {...sourceProps} />
            </FileToolbar>
            <FileList />
            <FileContextMenu />
          </FileBrowser>
        </Grid>
        <Grid className="browser" item xs={6}>
          <FileBrowser {...targetProps} ref={target}>
            <FileNavbar />
            <FileToolbar>
              <ToobarInfo {...targetProps} />
            </FileToolbar>
            <FileList />
            <FileContextMenu />
          </FileBrowser>
        </Grid>
      </Grid>
    </Box>
  );
};

function makeParam(priority: bigint, param: JobRestoreParam): JobCreateRequest {
  return {
    job: {
      priority,
      param: {
        param: {
          oneofKind: "restore",
          restore: param,
        },
      },
    },
  };
}
