import { useState, useEffect, useMemo, useCallback, FC, useRef, RefObject } from "react";
import moment from "moment";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import { FileBrowser, FileNavbar, FileToolbar, FileList, FileContextMenu, FileArray, FileBrowserHandle } from "@aperturerobotics/chonky";
import { ChonkyActions, ChonkyFileActionData, FileData } from "@aperturerobotics/chonky";

import { cli, Root } from "../api";
import { TapeListRequest, Source, Tape } from "../entity";

export const TapesType = "tapes";

const convertTapes = (tapes: Array<Tape>): FileData[] => {
  return tapes.map((tape) => {
    // const isDir = (file.mode & ModeDir) > 0;

    return {
      id: `${tape.id}`,
      name: tape.barcode,
      ext: "",
      isDir: true,
      isHidden: false,
      openable: false,
      selectable: true,
      draggable: true,
      droppable: false,
      size: 0,
      modDate: moment.unix(Number(tape.createTime)).toDate(),
    };
  });
};

const useTapesSourceBrowser = (source: RefObject<FileBrowserHandle>) => {
  const [files, setFiles] = useState<FileArray>(Array(1).fill(null));
  const [folderChain, setFolderChan] = useState<FileArray>([Root]);

  const openFolder = useCallback(async (id: string) => {
    const reply = await cli.tapeList({ param: { oneofKind: "list", list: { offset: 0n, limit: 1000n } } }).response;

    setFiles(convertTapes(reply.tapes));
    setFolderChan([Root]);
  }, []);
  useEffect(() => {
    openFolder(Root.id);
  }, []);

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
        case ChonkyActions.DeleteFiles.id:
          (async () => {
            await cli.tapeDelete({ ids: data.state.selectedFiles.map((file) => BigInt(file.id)) });
          })();
          return;
      }
    },
    [openFolder, source, folderChain],
  );

  const fileActions = useMemo(() => [ChonkyActions.DeleteFiles], []);

  return {
    files,
    folderChain,
    onFileAction,
    fileActions,
    defaultFileViewActionId: ChonkyActions.EnableListView.id,
    doubleClickDelay: 300,
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
