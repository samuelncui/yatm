import { useState, useEffect, useMemo, useCallback, FC, useRef, RefObject } from "react";
import { toast } from "react-toastify";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import { FileBrowser, FileNavbar, FileToolbar, FileList, FileContextMenu, FileArray, FileBrowserHandle, defaultFormatters } from "@samuelncui/chonky";
import { ChonkyActions, ChonkyFileActionData, FileData } from "@samuelncui/chonky";

import { ToobarInfo } from "../components/toolbarInfo";

import { Root, cli, convertFiles } from "../api";
import { AddFileAction, RefreshListAction, CreateRestoreJobAction, GetDataUsageAction } from "../actions";
import { JobCreateRequest, JobRestoreParam, Source } from "../entity";
import { chonkyI18n } from "../tools";

const useRestoreSourceBrowser = (targetFiles: FileArray, target: RefObject<FileBrowserHandle>) => {
  const [files, setFiles] = useState<FileArray>(Array(1).fill(null));
  const [folderChain, setFolderChan] = useState<FileArray>([Root]);
  const currentID = useMemo(() => {
    if (folderChain.length === 0) {
      return "0";
    }

    const last = folderChain.slice(-1)[0];
    if (!last) {
      return "0";
    }

    return last.id;
  }, [folderChain]);

  const openFolder = useCallback(
    async (id: string, needSize: boolean = false) => {
      const [file, folderChain] = await Promise.all([cli.fileGet({ id: BigInt(id), needSize }).response, cli.fileListParents({ id: BigInt(id) }).response]);

      setFiles(convertFiles(file.children, needSize).map((file) => ({ ...file, droppable: false })));
      setFolderChan([Root, ...convertFiles(folderChain.parents, needSize)]);
    },
    [setFiles, setFolderChan],
  );
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
        case GetDataUsageAction.id:
          openFolder(currentID, true);
          return;
        case ChonkyActions.EndDragNDrop.id:
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

          target.current.requestFileAction(AddFileAction, { ...data.payload, selectedFiles });
          return;
      }
    },
    [openFolder, target, folderChain, currentID],
  );

  const fileActions = useMemo(() => [GetDataUsageAction, ChonkyActions.StartDragNDrop, RefreshListAction], []);

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
    id: "restore_waitlist",
    name: "Restore Waitlist",
    isDir: true,
    openable: true,
    selectable: true,
    draggable: true,
    droppable: true,
  },
] as FileArray;

const useRestoreTargetBrowser = () => {
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

              const reply = await cli.fileGet({ id: BigInt(file.id), needSize: true }).response;
              onFileSizeUpdatedRef.current(file.id, Number(reply.file?.size));
            }
          })();

          return;
        case CreateRestoreJobAction.id:
          (async () => {
            const fileIds = files.filter((file): file is FileData => !!file && file.id !== "0").map((file) => BigInt(file.id));
            console.log(await cli.jobCreate(makeParam(1n, { fileIds })).response);

            toast.success("Create Restore Job Success!");
          })();
          return;
      }
    },
    [files, setFiles],
  );

  const fileActions = useMemo(() => [ChonkyActions.DeleteFiles, AddFileAction, CreateRestoreJobAction], []);

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

export const RestoreType = "restore";

export const RestoreBrowser = () => {
  const target = useRef<FileBrowserHandle>(null);
  const targetProps = useRestoreTargetBrowser();
  const sourceProps = useRestoreSourceBrowser(targetProps.files, target);

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
