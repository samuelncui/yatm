import { useState, useEffect, useMemo, useCallback, FC } from "react";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import { FullFileBrowser, FileBrowser, FileNavbar, FileToolbar, FileList, FileContextMenu, FileArray } from "chonky";
import { ChonkyActions, ChonkyFileActionData } from "chonky";

import { DndProvider as UntypedDndProvider, useDrop, DndProviderProps } from "react-dnd";
import { HTML5Backend } from "react-dnd-html5-backend";

import "./app.less";
import { cli, convertSourceFiles } from "./api";
import { Root } from "./api";
import { RenameFileAction, RefreshListAction } from "./actions";

import { useDetailModal, DetailModal, Detail } from "./detail";

const DndProvider = UntypedDndProvider as FC<DndProviderProps<any, any> & { children: JSX.Element[] }>;

const useBackupSourceBrowser = () =>
  // openDetailModel: (detail: Detail) => void
  {
    const [files, setFiles] = useState<FileArray>(Array(1).fill(null));
    const [folderChain, setFolderChan] = useState<FileArray>([Root]);
    // const currentID = useMemo(() => {
    //   if (folderChain.length === 0) {
    //     return "0";
    //   }

    //   const last = folderChain.slice(-1)[0];
    //   if (!last) {
    //     return "0";
    //   }

    //   return last.id;
    // }, [folderChain]);

    const openFolder = useCallback((path: string) => {
      (async () => {
        const result = await cli.sourceList({ path }).response;
        console.log("source list", {
          path,
          result,
          converted: convertSourceFiles(result.children),
        });

        setFiles(convertSourceFiles(result.children));
        setFolderChan(convertSourceFiles(result.chain));
      })();
    }, []);
    useEffect(() => openFolder(""), []);

    const onFileAction = useCallback(
      (data: ChonkyFileActionData) => {
        // console.log(data);
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

              // const file = await getFile(fileToOpen.id);
              // await openDetailModel(file);
            })();

            return;
          // case ChonkyActions.MoveFiles.id:
          //   (async () => {
          //     const { destination, files } = data.payload;
          //     for (const file of files) {
          //       await editFile(file.id, { parentid: destination.id });
          //     }
          //     await refreshAll();
          //   })();

          //   return;
          // case RenameFileAction.id:
          //   (async () => {
          //     const files = data.state.selectedFilesForAction;
          //     if (files.length === 0) {
          //       return;
          //     }
          //     const file = files[0];

          //     const name = prompt("Provide new name for this file:", file.name);
          //     if (!name) {
          //       return;
          //     }

          //     await editFile(file.id, { name });
          //     await refreshAll();
          //   })();
          //   return;
          // case ChonkyActions.CreateFolder.id:
          //   (async () => {
          //     const name = prompt("Provide the name for your new folder:");
          //     if (!name) {
          //       return;
          //     }

          //     await createFolder(currentID, { name });
          //     await refreshAll();
          //   })();
          //   return;
          // case ChonkyActions.DeleteFiles.id:
          //   (async () => {
          //     const files = data.state.selectedFilesForAction;
          //     const fileids = files.map((file) => file.id);
          //     await deleteFolder(fileids);
          //     await refreshAll();
          //   })();

          //   return;
          // case RefreshListAction.id:
          //   openFolder(currentID);
          //   return;
        }
      },
      [openFolder]
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

const useBackupTargetBrowser = () =>
  // openDetailModel: (detail: Detail) => void
  {
    const [files, setFiles] = useState<FileArray>(Array(1).fill(null));
    const [folderChain, setFolderChan] = useState<FileArray>([Root]);
    // const currentID = useMemo(() => {
    //   if (folderChain.length === 0) {
    //     return "0";
    //   }

    //   const last = folderChain.slice(-1)[0];
    //   if (!last) {
    //     return "0";
    //   }

    //   return last.id;
    // }, [folderChain]);

    const openFolder = useCallback((path: string) => {
      (async () => {
        const result = await cli.sourceList({ path }).response;
        result.chain[0].name = "BackupSource";

        setFiles(convertSourceFiles(result.children));
        setFolderChan(convertSourceFiles(result.chain));
      })();
    }, []);
    useEffect(() => openFolder(""), []);

    const onFileAction = useCallback(
      (data: ChonkyFileActionData) => {
        // console.log(data);
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

              // const file = await getFile(fileToOpen.id);
              // await openDetailModel(file);
            })();

            return;
          // case ChonkyActions.MoveFiles.id:
          //   (async () => {
          //     const { destination, files } = data.payload;
          //     for (const file of files) {
          //       await editFile(file.id, { parentid: destination.id });
          //     }
          //     await refreshAll();
          //   })();

          //   return;
          // case RenameFileAction.id:
          //   (async () => {
          //     const files = data.state.selectedFilesForAction;
          //     if (files.length === 0) {
          //       return;
          //     }
          //     const file = files[0];

          //     const name = prompt("Provide new name for this file:", file.name);
          //     if (!name) {
          //       return;
          //     }

          //     await editFile(file.id, { name });
          //     await refreshAll();
          //   })();
          //   return;
          // case ChonkyActions.CreateFolder.id:
          //   (async () => {
          //     const name = prompt("Provide the name for your new folder:");
          //     if (!name) {
          //       return;
          //     }

          //     await createFolder(currentID, { name });
          //     await refreshAll();
          //   })();
          //   return;
          // case ChonkyActions.DeleteFiles.id:
          //   (async () => {
          //     const files = data.state.selectedFilesForAction;
          //     const fileids = files.map((file) => file.id);
          //     await deleteFolder(fileids);
          //     await refreshAll();
          //   })();

          //   return;
          // case RefreshListAction.id:
          //   openFolder(currentID);
          //   return;
        }
      },
      [openFolder]
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

// const CustomDropZone = () => {
//   const [maybeImpostor, setMaybeImpostor] = useState<string | null>(null);
//   const [{ isOver, canDrop }, drop] = useDrop({
//     accept: ChonkyDndFileEntryType,
//     drop: (item: ChonkyDndFileEntryItem) => {
//       setMaybeImpostor(item.payload.draggedFile.name);
//       console.log("DnD payload:", item.payload);
//     },
//     // canDrop: (item: ChonkyDndFileEntryItem) => !item.payload.draggedFile.isDir,
//     canDrop: (item: ChonkyDndFileEntryItem) => true,
//     collect: (monitor) => ({
//       isOver: monitor.isOver(),
//       canDrop: monitor.canDrop(),
//     }),
//   });
//   return (
//     <div
//       ref={drop}
//       style={{
//         boxShadow: "inset rgba(0, 0, 0, 0.6) 0 100px 0",
//         backgroundImage: "url(./shadow-realm.gif)",
//         lineHeight: "100px",
//         textAlign: "center",
//         fontSize: "1.4em",
//         marginBottom: 20,
//         borderRadius: 4,
//         color: "#fff",
//         height: 100,
//       }}
//     >
//       {isOver
//         ? canDrop
//           ? "C'mon, drop 'em!"
//           : "Folders are not allowed!"
//         : maybeImpostor
//         ? `${maybeImpostor} was not the impostor.`
//         : "Drag & drop a (Chonky) file here"}
//     </div>
//   );
// };

export const BackupType = "backup";

export const BackupBrowser = () => {
  const sourceProps = useBackupSourceBrowser();
  const targetProps = useBackupTargetBrowser();

  return (
    <Box className="browser-box">
      <Grid className="browser-container" container>
        <Grid className="browser" item xs={6}>
          {/* <CustomDropZone /> */}
          <FullFileBrowser {...sourceProps} />
        </Grid>
        <Grid className="browser" item xs={6}>
          <FileBrowser {...targetProps}>
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
