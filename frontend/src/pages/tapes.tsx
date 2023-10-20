import { useState, useEffect, useMemo, useCallback, FC, useRef, RefObject } from "react";
import { toast } from "react-toastify";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import { FileBrowser, FileNavbar, FileToolbar, FileList, FileContextMenu, FileArray, FileBrowserHandle } from "@samuelncui/chonky";
import { ChonkyActions, ChonkyFileActionData, FileData } from "@samuelncui/chonky";

import { cli, Root, convertTapes, convertPositions } from "../api";
import { TrimLibraryAction } from "../actions";
import { chonkyI18n } from "../tools";

export const TapesType = "tapes";

const useTapesSourceBrowser = (source: RefObject<FileBrowserHandle>) => {
  const [files, setFiles] = useState<FileArray>(Array(1).fill(null));
  const [folderChain, setFolderChan] = useState<FileArray>([Root]);
  const current = useMemo(() => {
    if (folderChain.length === 0) {
      return Root;
    }

    const last = folderChain.slice(-1)[0];
    if (!last) {
      return Root;
    }

    return last;
  }, [folderChain]);

  const openFolder = useCallback(
    async (target: FileData) => {
      if (target.id === Root.id) {
        const reply = await cli.tapeList({ param: { oneofKind: "list", list: { offset: 0n, limit: 1000n } } }).response;

        setFiles(convertTapes(reply.tapes));
        setFolderChan([Root]);
        return;
      }

      const id = target.id;
      var tapeIDStr = id;
      var dir = "";

      const splitIdx = tapeIDStr.indexOf(":");
      if (splitIdx >= 0) {
        dir = tapeIDStr.slice(splitIdx + 1);
        tapeIDStr = tapeIDStr.slice(0, splitIdx);
      }

      const reply = await cli.tapeGetPositions({ id: BigInt(tapeIDStr), directory: dir }).response;
      const files = convertPositions(reply.positions);
      setFiles(files);

      const targetFolderChain = [];
      for (const folder of folderChain) {
        if (!folder) {
          continue;
        }
        if (folder.id === target.id) {
          targetFolderChain.push(folder);
          setFolderChan(targetFolderChain);
          return;
        }

        targetFolderChain.push(folder);
      }

      targetFolderChain.push(target);
      setFolderChan(targetFolderChain);
      return;
    },
    [folderChain],
  );
  useEffect(() => {
    openFolder(Root);
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
              await openFolder(fileToOpen);
              return;
            }
          })();

          return;
        case ChonkyActions.DeleteFiles.id:
          (async () => {
            const targetTapes = data.state.selectedFiles;
            if (!confirm(`Following tapes will be deleted, may cause data loss. Are you sure?\n${targetTapes.map((tape) => tape.name).join(", ")}`)) {
              return;
            }
            await cli.tapeDelete({ ids: targetTapes.filter((file) => file.isTape).map((file) => BigInt(file.id)) });
            await openFolder(current);
          })();
          return;
        case TrimLibraryAction.id:
          (async () => {
            if (!confirm(`Empty pointer in library will be trimed, may cause data loss. Are you sure?`)) {
              return;
            }

            console.log(await cli.libraryTrim({ trimFile: true, trimPosition: true }).response);
            toast.success("Trim Library Success!");
          })();
          return;
      }
    },
    [openFolder, source, folderChain],
  );

  const fileActions = useMemo(() => [ChonkyActions.DeleteFiles, TrimLibraryAction], []);

  return {
    files,
    folderChain,
    onFileAction,
    fileActions,
    defaultFileViewActionId: ChonkyActions.EnableListView.id,
    doubleClickDelay: 300,
    i18n: chonkyI18n,
  };
};

export const TapesBrowser = () => {
  const target = useRef<FileBrowserHandle>(null);
  const sourceProps = useTapesSourceBrowser(target);

  return (
    <Box className="browser-box">
      <Grid className="browser-container" container>
        <Grid className="browser" item xs={12}>
          <FileBrowser {...sourceProps}>
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
