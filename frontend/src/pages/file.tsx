import { useState, useRef, useEffect, useMemo, useCallback } from "react";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import { FileBrowser as ChonckFileBrowser, FileNavbar, FileToolbar, FileList, FileContextMenu, FileArray, FileBrowserHandle } from "@samuelncui/chonky";
import { ChonkyActions, ChonkyFileActionData } from "@samuelncui/chonky";

import { cli, convertFiles } from "../api";
import { Root } from "../api";
import { RenameFileAction, RefreshListAction, GetDataUsageAction, CreateFolder } from "../actions";
import { ToobarInfo } from "../components/toolbarInfo";

import { useDetailModal, DetailModal } from "./file-detail";
import { FileGetReply } from "../entity";
import { chonkyI18n } from "../tools";

const useDualSide = () => {
  const left = useRef<FileBrowserHandle>(null);
  const right = useRef<FileBrowserHandle>(null);
  const instances = { left, right };

  const refreshAll = useCallback(async () => {
    await Promise.all(
      Object.values(instances).map((ref) => {
        if (!ref || !ref.current) {
          return;
        }
        return ref.current.requestFileAction(RefreshListAction, {});
      }),
    );
  }, [instances]);

  return { instances, refreshAll };
};

const useFileBrowser = (storageKey: string, refreshAll: () => Promise<void>, openDetailModel: (detail: FileGetReply) => void) => {
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

      setFiles(convertFiles(file.children, needSize));
      setFolderChan([Root, ...convertFiles(folderChain.parents, needSize)]);
      localStorage.setItem(storageKey, id);
    },
    [setFiles, setFolderChan],
  );
  useEffect(() => {
    (async () => {
      const storagedID = localStorage.getItem(storageKey);
      if (storagedID) {
        try {
          await openFolder(storagedID);
          return;
        } catch (e) {
          console.log("open storaged id fail, err= ", e);
        }
      }

      openFolder(Root.id);
    })();
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

            const file = await cli.fileGet({ id: BigInt(fileToOpen.id) }).response;
            await openDetailModel(file);
          })();

          return;
        case ChonkyActions.MoveFiles.id:
          (async () => {
            const { destination, files } = data.payload;
            for (const file of files) {
              await cli.fileEdit({
                id: BigInt(file.id),
                file: { parentId: BigInt(destination.id) },
              }).response;
            }
            await refreshAll();
          })();

          return;
        case RenameFileAction.id:
          (async () => {
            const files = data.state.selectedFilesForAction;
            if (files.length === 0) {
              return;
            }
            const file = files[0];

            const name = prompt("Provide new name for this file:", file.name);
            if (!name) {
              return;
            }

            await cli.fileEdit({ id: BigInt(file.id), file: { name } }).response;
            await refreshAll();
          })();
          return;
        case CreateFolder.id:
          (async () => {
            const name = prompt("Provide the name for your new folder:");
            if (!name) {
              return;
            }

            await cli.fileMkdir({ parentId: BigInt(currentID), path: name }).response;
            await refreshAll();
          })();
          return;
        case ChonkyActions.DeleteFiles.id:
          (async () => {
            const files = data.state.selectedFilesForAction;
            const fileids = files.map((file) => BigInt(file.id));
            await cli.fileDelete({ ids: fileids }).response;
            await refreshAll();
          })();

          return;
        case GetDataUsageAction.id:
          openFolder(currentID, true);
          return;
        case RefreshListAction.id:
          openFolder(currentID);
          return;
      }
    },
    [openFolder, openDetailModel, refreshAll, currentID],
  );

  const fileActions = useMemo(
    () => [CreateFolder, GetDataUsageAction, ChonkyActions.DeleteFiles, ChonkyActions.MoveFiles, RenameFileAction, RefreshListAction],
    [],
  );
  const totalSize = useMemo(() => {
    return files.reduce((total, file) => total + (file?.size ? file.size : 0), 0);
  }, [files]);

  return {
    files,
    folderChain,
    onFileAction,
    fileActions,
    defaultFileViewActionId: ChonkyActions.EnableListView.id,
    doubleClickDelay: 300,
    totalSize,
    i18n: chonkyI18n,
  };
};

export const FileBrowserType = "file";

export const FileBrowser = () => {
  const { instances, refreshAll } = useDualSide();
  const { detail, openDetailModel, closeDetailModel } = useDetailModal();

  const leftProps = useFileBrowser("file_browser:left:current_id", refreshAll, openDetailModel);
  const rightProps = useFileBrowser("file_browser:right:current_id", refreshAll, openDetailModel);

  useEffect(() => {
    Object.values(instances).map((inst) => inst.current?.requestFileAction(ChonkyActions.ToggleHiddenFiles, {}));
    const interval = setInterval(() => {
      Object.values(instances).map((inst) => inst.current && inst.current.requestFileAction(RefreshListAction, {}));
    }, 10000);
    return () => clearInterval(interval);
  }, []);

  return (
    <Box className="browser-box">
      <Grid className="browser-container" container>
        <Grid className="browser" item xs={6}>
          <ChonckFileBrowser instanceId="left" ref={instances.left} {...leftProps}>
            <FileNavbar />
            <FileToolbar>
              <ToobarInfo files={leftProps.files} />
            </FileToolbar>
            <FileList />
            <FileContextMenu />
          </ChonckFileBrowser>
        </Grid>
        <Grid className="browser" item xs={6}>
          <ChonckFileBrowser instanceId="right" ref={instances.right} {...rightProps}>
            <FileNavbar />
            <FileToolbar>
              <ToobarInfo files={rightProps.files} />
            </FileToolbar>
            <FileList />
            <FileContextMenu />
          </ChonckFileBrowser>
        </Grid>
      </Grid>
      <DetailModal detail={detail} onClose={closeDetailModel} />
    </Box>
  );
};
