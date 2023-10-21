import { useState, useEffect, useMemo, useCallback, FC, useRef, RefObject } from "react";
import { toast } from "react-toastify";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import { FileBrowser, FileNavbar, FileToolbar, FileList, FileContextMenu, FileArray, FileBrowserHandle } from "@samuelncui/chonky";
import { ChonkyActions, ChonkyFileActionData, FileData } from "@samuelncui/chonky";

import { cli, convertSourceFiles } from "../api";
import { Root } from "../api";
import { AddFileAction, RefreshListAction, CreateBackupJobAction } from "../actions";
import { JobArchiveParam, JobCreateRequest, Source } from "../entity";
import { chonkyI18n } from "../tools";
import { ToobarInfo } from "../components/toolbarInfo";

const useBackupSourceBrowser = (targetFiles: FileArray, source: RefObject<FileBrowserHandle>) => {
  const [files, setFiles] = useState<FileArray>(Array(1).fill(null));
  const [folderChain, setFolderChain] = useState<FileArray>([Root]);

  const openFolder = useCallback(
    (path: string) => {
      (async () => {
        const result = await cli.sourceList({ path }).response;

        setFiles(convertSourceFiles(result.children));
        setFolderChain(convertSourceFiles(result.chain));
      })();
    },
    [targetFiles, setFiles, setFolderChain],
  );
  useEffect(() => openFolder(""), []);

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
          if (!source.current) {
            return;
          }

          const selectedFiles = data.payload.selectedFiles.map((file) => ({
            ...file,
            name: file.id,
            openable: false,
            draggable: false,
          }));

          source.current.requestFileAction(AddFileAction, { ...data.payload, selectedFiles });
          return;
      }
    },
    [openFolder, source],
  );

  const fileActions = useMemo(() => [ChonkyActions.StartDragNDrop, RefreshListAction], []);

  return {
    files: useMemo(() => {
      const targetFileIDs = new Set((targetFiles.filter((f) => !!f) as FileData[]).map((f) => f.id));
      const getDragable = !!folderChain.find((file) => file && targetFileIDs.has(file.id))
        ? (_: FileData) => false
        : (file: FileData) => !targetFileIDs.has(file.id);

      return files.map((file) => {
        if (!file) {
          return file;
        }

        const draggable = getDragable(file);
        return { ...file, droppable: false, draggable, selectable: draggable };
      });
    }, [files, folderChain, targetFiles]),
    folderChain,
    onFileAction,
    fileActions,
    defaultFileViewActionId: ChonkyActions.EnableListView.id,
    doubleClickDelay: 300,
    i18n: chonkyI18n,
  };
};

const targetFolderChain = [
  {
    id: "backup_waitlist",
    name: "Backup Waitlist",
    isDir: true,
    openable: true,
    selectable: true,
    draggable: true,
    droppable: true,
  },
] as FileArray;

const useBackupTargetBrowser = () => {
  const [files, setFiles] = useState<FileArray>(Array(0));

  const onFileSizeUpdated = useCallback(
    (id: string, size: number) => {
      setFiles(
        (files.filter((file) => !!file) as FileData[]).map((file: FileData) => {
          if (file.id === id) {
            return { ...file, size };
          }

          return file;
        }),
      );
    },
    [files, setFiles],
  );
  const onFileSizeUpdatedRef = useRef(onFileSizeUpdated);
  onFileSizeUpdatedRef.current = onFileSizeUpdated;

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
          const addedFiles = (data.payload as any)?.selectedFiles as FileData[];
          setFiles([...files, ...addedFiles]);

          (async () => {
            for (const file of addedFiles) {
              if (!file) {
                continue;
              }
              if (file.size !== undefined) {
                continue;
              }

              const reply = await cli.sourceGetSize({ path: file.id }).response;
              onFileSizeUpdatedRef.current(file.id, Number(reply.size));
            }
          })();

          return;
        case CreateBackupJobAction.id:
          (async () => {
            const sources = files
              .map((file) => {
                if (!file) {
                  console.log('create backup job, cannot get file')
                  return;
                }

                let path = file.id.trim();
                if (path.length === 0) {
                  console.log('create backup job, file id is too short', file)
                  return;
                }
                while (path.endsWith("/")) {
                  path = path.slice(0, -1);
                }

                let splitIdx = path.lastIndexOf("/");
                if (splitIdx < 0) {
                  splitIdx = -1
                }

                return { base: path.slice(0, splitIdx + 1), path: [path.slice(splitIdx + 1)] } as Source;
              })
              .filter((source): source is Source => !!source);

            const req = makeArchiveParam(1n, { sources });
            console.log(req, await cli.jobCreate(req).response);
            toast.success("Create Backup Job Success!");
          })();
          return;
      }
    },
    [files, setFiles],
  );

  const fileActions = useMemo(() => [ChonkyActions.DeleteFiles, AddFileAction, CreateBackupJobAction], []);

  return {
    files,
    folderChain: targetFolderChain,
    onFileAction,
    fileActions,
    defaultFileViewActionId: ChonkyActions.EnableListView.id,
    doubleClickDelay: 300,
    i18n: chonkyI18n,
  };
};

export const BackupType = "backup";

export const BackupBrowser = () => {
  const target = useRef<FileBrowserHandle>(null);
  const targetProps = useBackupTargetBrowser();
  const sourceProps = useBackupSourceBrowser(targetProps.files, target);

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
            <FileToolbar>
              <ToobarInfo files={targetProps.files} />
            </FileToolbar>
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
