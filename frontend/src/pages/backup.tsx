import { useState, useEffect, useMemo, useCallback, FC, useRef, RefObject } from "react";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import { FileBrowser, FileNavbar, FileToolbar, FileList, FileContextMenu, FileArray, FileBrowserHandle } from "chonky";
import { ChonkyActions, ChonkyFileActionData, FileData } from "chonky";

import { cli, convertSourceFiles } from "../api";
import { Root } from "../api";
import { AddFileAction, RefreshListAction, CreateBackupJobAction } from "../actions";
import { JobArchiveParam, JobCreateRequest, Source } from "../entity";

const useBackupSourceBrowser = (source: RefObject<FileBrowserHandle>) => {
  const [files, setFiles] = useState<FileArray>(Array(1).fill(null));
  const [folderChain, setFolderChan] = useState<FileArray>([Root]);

  const openFolder = useCallback((path: string) => {
    (async () => {
      const result = await cli.sourceList({ path }).response;

      setFiles(convertSourceFiles(result.children));
      setFolderChan(convertSourceFiles(result.chain));
    })();
  }, []);
  useEffect(() => openFolder(""), []);

  const onFileAction = useCallback(
    (data: ChonkyFileActionData) => {
      console.log("source", data);
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
          if (!source.current) {
            return;
          }

          source.current.requestFileAction(AddFileAction, data.payload);
          return;
      }
    },
    [openFolder, source]
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

const useBackupTargetBrowser = () => {
  const [files, setFiles] = useState<FileArray>(Array(0));
  const [folderChain, setFolderChan] = useState<FileArray>([
    {
      id: "0",
      name: "Backup Waitlist",
      isDir: true,
      openable: true,
      selectable: true,
      draggable: true,
      droppable: true,
    },
  ]);

  const onFileAction = useCallback(
    (data: ChonkyFileActionData) => {
      console.log("target", data);
      switch (data.id) {
        case ChonkyActions.DeleteFiles.id:
          (() => {
            const remotedIDs = new Set(data.state.selectedFiles.map((file) => file.id));
            setFiles([...files.filter((file) => file && !remotedIDs.has(file.id))]);
          })();
          return;
        case AddFileAction.id:
          setFiles([
            ...files,
            ...((data.payload as any)?.selectedFiles as FileData[]).map((file) => ({ ...file, name: file.id, openable: false, draggable: false })),
          ]);
          return;
        case CreateBackupJobAction.id:
          (async () => {
            const sources = files
              .map((file) => {
                if (!file) {
                  return undefined;
                }

                let path = file.id.trim();
                if (path.length === 0) {
                  return;
                }
                while (path.endsWith("/")) {
                  path = path.slice(0, -1);
                }
                const splitIdx = path.lastIndexOf("/");
                if (splitIdx < 0) {
                  return;
                }

                return { base: path.slice(0, splitIdx + 1), path: [path.slice(splitIdx + 1)] } as Source;
              })
              .filter((source): source is Source => !!source);

            const req = makeArchiveParam(1n, { sources });
            console.log(req, await cli.jobCreate(req).response);
            alert("Create Backup Job Success!");
          })();
          return;
      }
    },
    [files, setFiles]
  );

  const fileActions = useMemo(() => [ChonkyActions.DeleteFiles, AddFileAction, CreateBackupJobAction], []);

  return {
    files,
    folderChain,
    onFileAction,
    fileActions,
    defaultFileViewActionId: ChonkyActions.EnableListView.id,
    doubleClickDelay: 300,
  };
};

export const BackupType = "backup";

export const BackupBrowser = () => {
  const target = useRef<FileBrowserHandle>(null);
  const sourceProps = useBackupSourceBrowser(target);
  const targetProps = useBackupTargetBrowser();

  return (
    <Box className="browser-box">
      <Grid className="browser-container" container>
        <Grid className="browser" item xs={6}>
          <FileBrowser {...sourceProps}>
            <FileNavbar />
            <FileToolbar />
            <FileList />
            <FileContextMenu />
          </FileBrowser>
        </Grid>
        <Grid className="browser" item xs={6}>
          <FileBrowser {...targetProps} ref={target}>
            <FileNavbar />
            <FileToolbar />
            <FileList />
            <FileContextMenu />
          </FileBrowser>
        </Grid>
      </Grid>
    </Box>
  );
};

function makeArchiveParam(priority: bigint, param: JobArchiveParam): JobCreateRequest {
  return {
    job: {
      priority,
      param: {
        param: {
          oneofKind: "archive",
          archive: param,
        },
      },
    },
  };
}
