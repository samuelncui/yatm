import { ChangeEvent, Fragment, useCallback, useEffect, useRef, useState } from "react";
import { toast } from "react-toastify";
import { useNavigate } from "react-router";

import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogContentText from "@mui/material/DialogContentText";
import DialogTitle from "@mui/material/DialogTitle";
import Grid from "@mui/material/Grid";
import MenuItem from "@mui/material/MenuItem";
import TextField from "@mui/material/TextField";
import { ChonkyActions, ChonkyFileActionData, FileArray, FileBrowser, FileContextMenu, FileData, FileList, FileNavbar, FileToolbar } from "@samuelncui/chonky";

import { cli, convertMedia, convertPositions, isArchivePosition, fileCatalogCli, type MediaPositionFileData } from "@/api";
import {
  DeleteMediaAction,
  InitializeVolumeAction,
  InspectMediaAction,
  LoadMoreAction,
  ScanMediaAction,
  TrimLibraryAction,
  ViewArchiveCopiesAction,
  ImportPositionsAction,
  VerifyMediaAction,
  MediaJobsAction,
} from "@/actions";
import { MediaInspectResult, useMediaInspect } from "@/components/media-inspect";
import { MediaKind, VolumeType } from "@/entity";
import { chonkyI18n, runUIAction } from "@/tools";
import { ContentCopies } from "@/components/file-content";
import { RelatedJobs } from "@/components/related-jobs";
import { useActionDialog } from "@/components/action-dialog";

type MediaFileData = FileData & {
  isMedia?: boolean;
  mediaKind?: MediaKind;
  mediaIdentity?: string;
  mediaMounted?: boolean;
  mediaAvailableBytes?: bigint;
};

const isMediaFile = (file: FileData | null | undefined): file is MediaFileData => file?.isMedia === true;

type BrowserPage = { kind: "media"; offset: bigint } | { kind: "positions"; mediaID: string; directory: string; afterPath?: string };

const mediaPageSize = 100n;
const positionPageSize = 200n;

const MediaRoot: FileData = {
  id: "0",
  name: "Media",
  isDir: true,
  openable: true,
  selectable: true,
  draggable: false,
  droppable: false,
};

export const useMediaBrowser = (
  addVolume: () => void,
  scanMedia: (media: MediaFileData) => void,
  inspectMedia: (media: MediaFileData) => void,
  viewFile: (file: MediaPositionFileData) => void,
  verifyMedia: (media: MediaFileData) => void,
  relatedJobs: (media: MediaFileData) => void,
) => {
  const [files, setFiles] = useState<FileArray>(Array(1).fill(null));
  const [folderChain, setFolderChain] = useState<FileArray>([MediaRoot]);
  const [nextPage, setNextPage] = useState<BrowserPage | null>(null);
  const loading = useRef(false);
  const request = useRef(0);
  const { ask, dialog } = useActionDialog();

  const loadPage = useCallback(async (page: BrowserPage, append: boolean) => {
    const sequence = ++request.current;
    loading.current = true;
    try {
      if (page.kind === "media") {
        const reply = await cli.mediaList({
          param: { oneofKind: "list", list: { kinds: [], offset: page.offset, limit: mediaPageSize, query: "" } },
        }).response;
        if (sequence !== request.current) return;
        const converted = convertMedia(reply.media);
        setFiles((current) => (append ? [...current.filter(Boolean), ...converted] : converted));
        setNextPage(reply.hasMore ? { kind: "media", offset: page.offset + BigInt(reply.media.length) } : null);
        return;
      }

      const reply = await cli.mediaGetPositions({
        id: BigInt(page.mediaID),
        directory: page.directory,
        limit: positionPageSize,
        afterPath: page.afterPath,
      }).response;
      if (sequence !== request.current) return;
      const converted = convertPositions(reply.positions);
      setFiles((current) => (append ? [...current.filter(Boolean), ...converted] : converted));
      setNextPage(reply.hasMore ? { kind: "positions", mediaID: page.mediaID, directory: page.directory, afterPath: reply.positions.at(-1)?.path } : null);
    } finally {
      if (sequence === request.current) loading.current = false;
    }
  }, []);

  const openFolder = useCallback(
    async (target: FileData) => {
      if (target.id === MediaRoot.id) {
        setFolderChain([MediaRoot]);
        await loadPage({ kind: "media", offset: 0n }, false);
        return;
      }

      let mediaID = String(target.id);
      let directory = "";
      const separator = mediaID.indexOf(":");
      if (separator >= 0) {
        directory = mediaID.slice(separator + 1);
        mediaID = mediaID.slice(0, separator);
      }
      setFolderChain((current) => {
        const index = current.findIndex((folder) => folder?.id === target.id);
        return index >= 0 ? current.slice(0, index + 1) : [...current, target];
      });
      await loadPage({ kind: "positions", mediaID, directory }, false);
    },
    [loadPage],
  );

  useEffect(() => {
    runUIAction(() => openFolder(MediaRoot), "Load Media failed");
    return () => {
      request.current += 1;
      loading.current = false;
    };
  }, [openFolder]);

  const onFileAction = useCallback(
    (data: ChonkyFileActionData) => {
      switch (data.id) {
        case ChonkyActions.OpenFiles.id: {
          const file = data.payload.targetFile ?? data.payload.files[0];
          if (!file) return;
          if (file.isDir) {
            runUIAction(() => openFolder(file), "Open Media folder failed");
            return;
          }
          if (isArchivePosition(file)) viewFile(file);
          return;
        }
        case ChonkyActions.DeleteFiles.id: {
          const media = data.state.selectedFilesForAction.filter(isMediaFile);
          if (media.length === 0) return;
          ask({
            title: "Remove Media from Library?",
            confirmLabel: "Remove",
            danger: true,
            children: (
              <>
                <p>{media.map((value) => value.name).join(", ")}</p>
                <p>Archive files are kept. Their inventory records will be removed.</p>
              </>
            ),
            onConfirm: async () => {
              await cli.mediaDelete({ ids: media.map((value) => BigInt(value.id)) }).response;
              runUIAction(() => openFolder(MediaRoot), "Media removed, but the list could not refresh");
            },
          });
          return;
        }
        case InitializeVolumeAction.id:
          addVolume();
          return;
        case LoadMoreAction.id:
          if (nextPage && !loading.current) runUIAction(() => loadPage(nextPage, true), "Load more Media entries failed");
          return;
        case InspectMediaAction.id:
        case ScanMediaAction.id:
        case VerifyMediaAction.id:
        case MediaJobsAction.id: {
          const selected = (data.state.selectedFilesForAction ?? data.state.selectedFiles).filter(isMediaFile);
          if (selected.length !== 1) {
            toast.info("Select one Media.");
            return;
          }
          const handlers: Record<string, (media: MediaFileData) => void> = {
            [InspectMediaAction.id]: inspectMedia,
            [ScanMediaAction.id]: scanMedia,
            [VerifyMediaAction.id]: verifyMedia,
            [MediaJobsAction.id]: relatedJobs,
          };
          handlers[data.id](selected[0]);
          return;
        }
        case TrimLibraryAction.id:
          ask({
            title: "Clean up Library?",
            confirmLabel: "Clean up",
            danger: true,
            children: <p>Remove unreferenced inventory and files without originals or saved versions. Physical files are kept.</p>,
            onConfirm: async () => {
              await cli.libraryTrim({ trimFile: true, trimPosition: true }).response;
              toast.success("Library cleaned up");
              runUIAction(() => openFolder(MediaRoot), "Library cleaned up, but the list could not refresh");
            },
          });
          return;
        case ViewArchiveCopiesAction.id: {
          const selected = data.state.selectedFilesForAction[0];
          if (isArchivePosition(selected)) viewFile(selected);
          return;
        }
        case ImportPositionsAction.id: {
          const selected = data.state.selectedFilesForAction.filter(isArchivePosition);
          if (selected.length === 0) return;
          ask({
            title: "Add to Library?",
            confirmLabel: "Add files",
            children: (
              <p>
                Add {selected.length} archived {selected.length === 1 ? "file" : "files"}. Existing Library files are kept separately.
              </p>
            ),
            onConfirm: async () => {
              const reply = await fileCatalogCli.importPositions({ positionIds: selected.map((file) => file.positionID) }).response;
              toast.success(`Added ${reply.fileIds.length} Files to Library`);
            },
          });
          return;
        }
      }
    },
    [addVolume, inspectMedia, loadPage, nextPage, openFolder, scanMedia, viewFile, verifyMedia, relatedJobs, ask],
  );

  return {
    browserProps: {
      files,
      folderChain,
      onFileAction,
      fileActions: [
        InitializeVolumeAction,
        InspectMediaAction,
        ScanMediaAction,
        ViewArchiveCopiesAction,
        ImportPositionsAction,
        VerifyMediaAction,
        MediaJobsAction,
        DeleteMediaAction,
        TrimLibraryAction,
        ...(nextPage ? [LoadMoreAction] : []),
      ],
      defaultFileViewActionId: ChonkyActions.EnableListView.id,
      doubleClickDelay: 300,
      i18n: chonkyI18n,
    },
    refresh: () => openFolder(MediaRoot),
    dialog,
  };
};

export const AddVolumeDialog = ({ open, onClose, onAdded }: { open: boolean; onClose: () => void; onAdded: () => Promise<void> }) => {
  const [operation, setOperation] = useState<"initialize" | "register">("initialize");
  const [mountPoint, setMountPoint] = useState("");
  const [name, setName] = useState("");
  const [serialNumber, setSerialNumber] = useState("");
  const [type, setType] = useState(VolumeType.HDD);
  const [submitting, setSubmitting] = useState(false);

  const close = () => {
    if (!submitting) onClose();
  };
  const submit = async () => {
    if (!mountPoint.trim() || !name.trim()) return;
    setSubmitting(true);
    try {
      if (operation === "register") {
        await cli.volumeRegister({ mountPoint: mountPoint.trim(), name: name.trim() }).response;
        toast.success("Existing Volume registered");
      } else {
        await cli.volumeInitialize({
          mountPoint: mountPoint.trim(),
          name: name.trim(),
          profile: { serialNumber: serialNumber.trim(), type },
        }).response;
        toast.success("New Volume initialized");
      }
      await onAdded();
      onClose();
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onClose={close} maxWidth="sm" fullWidth>
      <DialogTitle>Add Volume</DialogTitle>
      <DialogContent>
        <TextField
          select
          margin="normal"
          label="Action"
          fullWidth
          value={operation}
          onChange={(event) => setOperation(event.target.value as "initialize" | "register")}
        >
          <MenuItem value="initialize">Initialize New</MenuItem>
          <MenuItem value="register">Register Existing</MenuItem>
        </TextField>
        <DialogContentText>
          {operation === "initialize"
            ? "Create a new identity marker and Library Media for an already mounted disk."
            : "Read an existing marker and recreate only its Library Media. Files and marker remain unchanged."}
        </DialogContentText>
        <TextField required margin="normal" label="Mount Point" fullWidth value={mountPoint} onChange={(event) => setMountPoint(event.target.value)} />
        <TextField required margin="normal" label="Name" fullWidth value={name} onChange={(event) => setName(event.target.value)} />
        {operation === "initialize" && (
          <>
            <TextField margin="normal" label="Serial Number" fullWidth value={serialNumber} onChange={(event) => setSerialNumber(event.target.value)} />
            <TextField
              select
              required
              margin="normal"
              label="Volume Type"
              fullWidth
              value={type}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setType(Number(event.target.value) as VolumeType)}
            >
              <MenuItem value={VolumeType.HDD}>HDD · concurrent random read/write</MenuItem>
              <MenuItem value={VolumeType.HM_SMR}>HM-SMR · concurrent random read, sequential write</MenuItem>
            </TextField>
          </>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={close}>Cancel</Button>
        <Button disabled={submitting || !mountPoint.trim() || !name.trim()} onClick={() => runUIAction(submit, "Add Volume failed")}>
          {operation === "initialize" ? "Initialize" : "Register"}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export const InspectMediaDialog = ({ media, onClose }: { media: MediaFileData; onClose: () => void }) => {
  const { reply, loading, error, inspect } = useMediaInspect();
  const [devices, setDevices] = useState<string[] | null>(null);
  const [device, setDevice] = useState("");

  useEffect(() => {
    if (media.mediaKind === MediaKind.VOLUME) {
      runUIAction(() => inspect({ oneofKind: "volume", volume: { uuid: media.mediaIdentity ?? "" } }), "Inspect Volume failed");
      return;
    }
    let active = true;
    runUIAction(async () => {
      const reply = await cli.deviceList({}).response;
      if (!active) return;
      setDevices(reply.devices);
      if (reply.devices.length === 1) {
        setDevice(reply.devices[0]);
        await inspect({ oneofKind: "tape", tape: { device: reply.devices[0] } });
      }
    }, "Load Tape devices failed");
    return () => {
      active = false;
    };
  }, [inspect, media.mediaIdentity, media.mediaKind]);

  const selectDevice = (value: string) => {
    setDevice(value);
    runUIAction(() => inspect({ oneofKind: "tape", tape: { device: value } }), "Inspect Tape failed");
  };

  return (
    <Dialog open onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>{media.name} · Properties</DialogTitle>
      <DialogContent>
        {media.mediaKind === MediaKind.TAPE && (
          <>
            <DialogContentText>Load the Tape into a drive to verify its physical identity.</DialogContentText>
            <TextField select required margin="normal" label="Drive Device" fullWidth value={device} onChange={(event) => selectDevice(event.target.value)}>
              {(devices ?? []).map((value) => (
                <MenuItem key={value} value={value}>
                  {value}
                </MenuItem>
              ))}
            </TextField>
            {devices?.length === 0 && <Alert severity="warning">No Tape drive is configured.</Alert>}
          </>
        )}
        <MediaInspectResult reply={reply} loading={loading} error={error} />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Close</Button>
      </DialogActions>
    </Dialog>
  );
};

export const MediaBrowser = () => {
  const navigate = useNavigate();
  const [addOpen, setAddOpen] = useState(false);
  const [inspectTarget, setInspectTarget] = useState<MediaFileData | null>(null);
  const [position, setPosition] = useState<MediaPositionFileData>();
  const [jobsMedia, setJobsMedia] = useState<MediaFileData>();
  const addVolume = useCallback(() => setAddOpen(true), []);
  const scanMedia = useCallback((media: MediaFileData) => navigate(`/scan?media=${media.id}`), [navigate]);
  const inspectMedia = useCallback((media: MediaFileData) => setInspectTarget(media), []);
  const viewFile = useCallback((file: MediaPositionFileData) => setPosition(file), []);
  const verifyMedia = useCallback((media: MediaFileData) => navigate(`/scan?media=${media.id}&result=verify`), [navigate]);
  const { browserProps, refresh, dialog: mediaDialog } = useMediaBrowser(addVolume, scanMedia, inspectMedia, viewFile, verifyMedia, setJobsMedia);

  return (
    <Fragment>
      {mediaDialog}
      <Box className="browser-box">
        <Grid className="browser-container" container columnSpacing={1.5}>
          <Grid className="browser" size={12}>
            <FileBrowser {...browserProps}>
              <FileNavbar />
              <FileToolbar />
              <FileList />
              <FileContextMenu />
            </FileBrowser>
          </Grid>
        </Grid>
      </Box>
      <AddVolumeDialog open={addOpen} onClose={() => setAddOpen(false)} onAdded={refresh} />
      {inspectTarget && <InspectMediaDialog media={inspectTarget} onClose={() => setInspectTarget(null)} />}
      <Dialog open={!!jobsMedia} onClose={() => setJobsMedia(undefined)} maxWidth="lg" fullWidth>
        <DialogTitle>{jobsMedia?.name} · Jobs</DialogTitle>
        <DialogContent>{jobsMedia && <RelatedJobs mediaID={BigInt(jobsMedia.id)} />}</DialogContent>
        <DialogActions>
          <Button onClick={() => setJobsMedia(undefined)}>Close</Button>
        </DialogActions>
      </Dialog>
      <Dialog open={!!position} onClose={() => setPosition(undefined)} maxWidth="sm" fullWidth>
        <DialogTitle>{position?.name}</DialogTitle>
        <DialogContent dividers>
          {position && position.signature.length > 0 ? <ContentCopies signature={position.signature} /> : <p>Content signature unknown.</p>}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setPosition(undefined)}>Close</Button>
        </DialogActions>
      </Dialog>
    </Fragment>
  );
};
