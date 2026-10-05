import { FileBrowser } from "@/components/file-browser";
import { ChangeEvent, Fragment, useCallback, useEffect, useId, useRef, useState } from "react";
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
import { CircularProgress, useMediaQuery, useTheme } from "@mui/material";
import StorageRoundedIcon from "@mui/icons-material/StorageRounded";
import Grid from "@mui/material/Grid";
import MenuItem from "@mui/material/MenuItem";
import TextField from "@mui/material/TextField";
import { ChonkyActions, ChonkyFileActionData, FileArray, FileContextMenu, FileData, FileList, FileNavbar, FileToolbar } from "@samuelncui/chonky";

import { cli, mediaCli, convertMedia, convertPositions, isArchivePosition, isMediaFile, filesCli, type MediaFileData, type MediaPositionFileData } from "@/api";
import {
  DeleteMediaAction,
  InitializeVolumeAction,
  InspectMediaAction,
  LoadMoreAction,
  ScanMediaAction,
  TrimLibraryAction,
  ImportPositionsAction,
  VerifyMediaAction,
  MediaJobsAction,
} from "@/actions";
import { MediaInspectResult, useMediaInspect } from "@/components/media-inspect";
import { MediaKind, VolumeCandidateState, VolumeType, type VolumeCandidate } from "@/entity";
import { chonkyI18n, errorMessage, runUIAction } from "@/tools";
import { DirectoryReadError } from "@/components/directory-read-error";
import { MediaInspector } from "@/components/media-inspector";
import { DetailDialog, DetailSurface } from "@/components/detail-surface";
import { ListPlaceholder } from "@/components/list-placeholder";
import { RelatedJobs } from "@/components/related-jobs";
import { useActionDialog } from "@/components/action-dialog";

const isArchiveInventoryEntry = (file: FileData | null | undefined): file is MediaPositionFileData => !!file && typeof file.position?.id === "bigint";

type BrowserPage = { kind: "media"; offset: bigint } | { kind: "positions"; mediaID: string; directory: string; afterPath?: string };

const mediaPageSize = 100n;
const positionPageSize = 200n;
const mediaSortActionIds = [ChonkyActions.SortFilesByName.id, ChonkyActions.SortFilesBySize.id, ChonkyActions.SortFilesByDate.id];

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
  selectFile: (row: MediaFileData | MediaPositionFileData | null) => void,
  verifyMedia: (media: MediaFileData) => void,
  relatedJobs: (media: MediaFileData) => void,
) => {
  const [files, setFiles] = useState<FileArray>([]);
  const [folderChain, setFolderChain] = useState<FileArray>([MediaRoot]);
  const [nextPage, setNextPage] = useState<BrowserPage | null>(null);
  const [panelLoading, setPanelLoading] = useState<"initial" | "refreshing" | "more" | undefined>("initial");
  const [loadError, setLoadError] = useState("");
  const loading = useRef(false);
  const request = useRef(0);
  const retryPage = useRef<BrowserPage>({ kind: "media", offset: 0n });
  const { ask, dialog } = useActionDialog();

  const loadPage = useCallback(async (page: BrowserPage, append: boolean, initial: boolean = false) => {
    const sequence = ++request.current;
    loading.current = true;
    retryPage.current = page;
    setLoadError("");
    setPanelLoading(append ? "more" : initial ? "initial" : "refreshing");
    try {
      if (page.kind === "media") {
        const reply = await mediaCli.list({
          param: { oneofKind: "list", list: { kinds: [], offset: page.offset, limit: mediaPageSize, query: "" } },
        }).response;
        if (sequence !== request.current) return;
        const converted = convertMedia(reply.media);
        setFiles((current) => (append ? [...current.filter(Boolean), ...converted] : converted));
        setNextPage(reply.hasMore ? { kind: "media", offset: page.offset + BigInt(reply.media.length) } : null);
        return;
      }

      const reply = await mediaCli.listPositions({
        id: BigInt(page.mediaID),
        directory: page.directory,
        limit: positionPageSize,
        afterPath: page.afterPath,
      }).response;
      if (sequence !== request.current) return;
      const converted = convertPositions(reply.positions);
      setFiles((current) => (append ? [...current.filter(Boolean), ...converted] : converted));
      setNextPage(reply.hasMore ? { kind: "positions", mediaID: page.mediaID, directory: page.directory, afterPath: reply.positions.at(-1)?.path } : null);
    } catch (error) {
      if (sequence !== request.current) return;
      const message = error instanceof Error ? error.message : String(error);
      if (append) {
        toast.error(`Could not load more Media: ${message}`);
      } else {
        setFiles([]);
        setNextPage(null);
        setLoadError(message);
      }
    } finally {
      if (sequence === request.current) {
        loading.current = false;
        setPanelLoading(undefined);
      }
    }
  }, []);

  const openFolder = useCallback(
    async (target: FileData) => {
      if (target.id === MediaRoot.id) {
        setFolderChain([MediaRoot]);
        setFiles([]);
        await loadPage({ kind: "media", offset: 0n }, false, true);
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
      setFiles([]);
      await loadPage({ kind: "positions", mediaID, directory }, false, true);
    },
    [loadPage],
  );

  useEffect(() => {
    void openFolder(MediaRoot);
    return () => {
      request.current += 1;
      loading.current = false;
    };
  }, [openFolder]);

  const onFileAction = useCallback(
    (data: ChonkyFileActionData) => {
      switch (data.id) {
        case ChonkyActions.OpenFiles.id: {
          // A recorded file has no bytes to open: the list selects it and the panel details it.
          const file = data.payload.targetFile ?? data.payload.files[0];
          if (!file?.isDir) return;
          selectFile(null);
          runUIAction(() => openFolder(file), "Open Media folder failed");
          return;
        }
        case ChonkyActions.ChangeSelection.id: {
          const selected = data.state.selectedFiles.length === 1 ? data.state.selectedFiles[0] : undefined;
          selectFile(selected && (isMediaFile(selected) || isArchivePosition(selected)) ? selected : null);
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
              await mediaCli.delete({ ids: media.map((value) => BigInt(value.id)), dryrun: false }).response;
              selectFile(null);
              void openFolder(MediaRoot);
            },
          });
          return;
        }
        case InitializeVolumeAction.id:
          addVolume();
          return;
        case LoadMoreAction.id:
          if (nextPage && !loading.current) void loadPage(nextPage, true);
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
              await cli.trim({ trimFile: true, trimPosition: true, dryrun: false }).response;
              toast.success("Library cleaned up");
              selectFile(null);
              void openFolder(MediaRoot);
            },
          });
          return;
        case ImportPositionsAction.id: {
          const selected = data.state.selectedFilesForAction.filter(isArchiveInventoryEntry);
          if (selected.length === 0) return;
          const directories = selected.filter((file) => file.isDir).length;
          const regularFiles = selected.length - directories;
          ask({
            title: "Add to Library?",
            confirmLabel: directories > 0 ? "Add folder contents" : "Add files",
            children: (
              <p>
                {directories > 0 && `${directories} ${directories === 1 ? "folder" : "folders"}`}
                {directories > 0 && regularFiles > 0 && " and "}
                {regularFiles > 0 && `${regularFiles} ${regularFiles === 1 ? "file" : "files"}`} will be added as independent Library files.
              </p>
            ),
            onConfirm: async () => {
              const reply = await filesCli.importPositions({ positionIds: selected.map((file) => file.position.id), dryrun: false }).response;
              if (reply.fileCount === 0n) {
                toast.info(reply.existingCount > 0n ? "These records are already in Library." : "The selected folders contain no files to add.");
                return;
              }
              const existing = reply.existingCount > 0n ? `; skipped ${reply.existingCount} already in Library` : "";
              const unsigned = reply.skippedFileCount > 0n ? `; skipped ${reply.skippedFileCount} without signatures` : "";
              toast.success(`Added ${reply.fileCount} Files to Library${existing}${unsigned}`);
            },
          });
          return;
        }
      }
    },
    [addVolume, inspectMedia, loadPage, nextPage, openFolder, scanMedia, selectFile, verifyMedia, relatedJobs, ask],
  );

  // The Media whose rows the list currently shows: the row under the Media root, which stays the
  // Position's Media however deep the list browsed.
  const media = folderChain.length > 1 ? (folderChain[1] as MediaFileData | undefined) : undefined;
  // Chonky sorts only the rows supplied to it, so sorting an incomplete page misstates the order
  // of the directory. All converted rows are visible; only Position folders mix files and dirs.
  const incompleteListing = !!nextPage || !!panelLoading || !!loadError;
  const disableDefaultFileActions = [
    ChonkyActions.ToggleHiddenFiles.id,
    ...(folderChain.length === 1 || incompleteListing ? [ChonkyActions.ToggleShowFoldersFirst.id] : []),
    ...(incompleteListing ? mediaSortActionIds : []),
    ...(incompleteListing ? [ChonkyActions.SelectAllFiles.id] : []),
  ];
  return {
    medium: media,
    browserProps: {
      files,
      folderChain,
      onFileAction,
      fileActions: [
        InitializeVolumeAction,
        InspectMediaAction,
        ScanMediaAction,
        ImportPositionsAction,
        VerifyMediaAction,
        MediaJobsAction,
        DeleteMediaAction,
        TrimLibraryAction,
        ...(nextPage ? [LoadMoreAction] : []),
      ],
      disableDefaultFileActions,
      doubleClickDelay: 300,
      i18n: chonkyI18n,
    },
    listProps: {
      // The initial read publishes its own placeholder instead of the empty list Chonky draws for
      // it; a continuation keeps its rows and Chonky's unobtrusive indicator.
      loading: panelLoading === "initial" ? undefined : panelLoading,
      loadingLabel: panelLoading === "more" ? "Loading more Media…" : "Loading Media…",
      emptyPlaceholder: loadError ? (
        <DirectoryReadError error={loadError} onRetry={() => loadPage(retryPage.current, false, true)} />
      ) : panelLoading ? (
        <ListPlaceholder loading label={folderChain.length > 1 ? "Reading this folder…" : "Reading Media…"} />
      ) : folderChain.length > 1 ? (
        <ListPlaceholder label="This folder is empty" />
      ) : (
        <ListPlaceholder label="No Media yet" />
      ),
    },
    refresh: () => {
      selectFile(null);
      return openFolder(MediaRoot);
    },
    dialog,
  };
};

const ManualVolumePath = "__manual__";

const volumeCandidateNotice = (candidate: VolumeCandidate): string => {
  switch (candidate.state) {
    case VolumeCandidateState.UNINITIALIZED:
      return "No YATM marker. Initialize creates this disk's identity marker and Library Media.";
    case VolumeCandidateState.UNREGISTERED:
      return "An existing marker was found. Register recreates only its Library Media; the marker and files stay unchanged.";
    case VolumeCandidateState.REGISTERED:
      return `Already in the Library as ${candidate.media?.name || candidate.name}.`;
    default:
      return candidate.detail || "This disk cannot be registered or initialized.";
  }
};

export const AddVolumeDialog = ({ open, onClose, onAdded }: { open: boolean; onClose: () => void; onAdded: () => Promise<void> }) => {
  const [operation, setOperation] = useState<"initialize" | "register">("initialize");
  const [mountPoint, setMountPoint] = useState("");
  const [name, setName] = useState("");
  const [serialNumber, setSerialNumber] = useState("");
  const [type, setType] = useState(VolumeType.HDD);
  const [submitting, setSubmitting] = useState(false);
  const [candidates, setCandidates] = useState<VolumeCandidate[]>([]);
  const [roots, setRoots] = useState<string[]>([]);
  const [selected, setSelected] = useState("");
  const [manual, setManual] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [loading, setLoading] = useState(true);
  const [discovered, setDiscovered] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const edited = useRef(false);

  // Discovery on opening or Retry never mounts or formats a disk.
  const applyCandidate = useCallback((candidate: VolumeCandidate) => {
    setSelected(candidate.mountPoint);
    setManual(false);
    setMountPoint(candidate.mountPoint);
    setName(candidate.name);
    setSerialNumber(candidate.profile?.serialNumber ?? "");
    setType(candidate.profile?.type === VolumeType.HM_SMR ? VolumeType.HM_SMR : VolumeType.HDD);
    setOperation(candidate.state === VolumeCandidateState.UNINITIALIZED ? "initialize" : "register");
  }, []);

  useEffect(() => {
    if (!open) return;
    // Every opening starts from the discovered set instead of the previous entry.
    edited.current = false;
    setDiscovered(false);
    setCandidates([]);
    setRoots([]);
    setSelected("");
    setManual(false);
    setLoadError("");
    setOperation("initialize");
    setMountPoint("");
    setName("");
    setSerialNumber("");
    setType(VolumeType.HDD);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    let active = true;
    setLoading(true);
    setLoadError("");
    void (async () => {
      try {
        const reply = await mediaCli.listVolumeCandidates({}).response;
        if (!active) return;
        setCandidates(reply.candidates);
        setRoots(reply.discoveryRoots);
        // A single candidate is the intended target; an empty list starts in manual mode.
        if (!edited.current) {
          if (reply.candidates.length === 1) applyCandidate(reply.candidates[0]);
          else if (reply.candidates.length === 0) setManual(true);
        }
      } catch (error) {
        if (active) {
          setLoadError(errorMessage(error, "Could not load mounted Volumes"));
          if (!edited.current) setManual(true);
        }
      } finally {
        if (active) {
          setLoading(false);
          setDiscovered(true);
        }
      }
    })();
    return () => {
      active = false;
    };
  }, [open, attempt, applyCandidate]);

  const fieldsDisabled = submitting || (loading && !discovered);

  const candidate = manual ? undefined : candidates.find((value) => value.mountPoint === selected);
  const blocked =
    !!candidate &&
    (candidate.state === VolumeCandidateState.REGISTERED ||
      candidate.state === VolumeCandidateState.CONFLICT ||
      candidate.state === VolumeCandidateState.INVALID);

  const close = () => {
    if (!submitting) onClose();
  };
  const submit = async () => {
    if (fieldsDisabled || !mountPoint.trim() || !name.trim() || blocked) return;
    setSubmitting(true);
    try {
      if (operation === "register") {
        await mediaCli.registerVolume({ mountPoint: mountPoint.trim(), name: name.trim() }).response;
        toast.success("Existing Volume registered");
      } else {
        await mediaCli.initializeVolume({
          mountPoint: mountPoint.trim(),
          name: name.trim(),
          profile: { serialNumber: serialNumber.trim(), type },
        }).response;
        toast.success("New Volume initialized");
      }
      onClose();
      runUIAction(onAdded, "Volume added, but the Media list could not refresh");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onClose={close} maxWidth="sm" fullWidth>
      <DialogTitle>Add Volume</DialogTitle>
      <DialogContent>
        {loading && <CircularProgress size={20} aria-label="Discovering mounted Volumes" />}
        {!manual && (
          <TextField
            select
            margin="normal"
            label="Disk"
            fullWidth
            disabled={fieldsDisabled}
            value={selected}
            onChange={(event) => {
              edited.current = true;
              const value = event.target.value;
              if (value === ManualVolumePath) {
                setManual(true);
                setSelected("");
                setMountPoint("");
                setName("");
                setSerialNumber("");
                setOperation("initialize");
                return;
              }
              const found = candidates.find((item) => item.mountPoint === value);
              if (found) applyCandidate(found);
            }}
          >
            <MenuItem value="" disabled>
              Choose a disk…
            </MenuItem>
            {candidates.map((value) => (
              <MenuItem key={value.mountPoint} value={value.mountPoint}>
                {value.mountPoint} · {value.state === VolumeCandidateState.REGISTERED ? value.media?.name || value.name : volumeStateLabel(value)}
                {value.profile?.serialNumber ? ` · SN ${value.profile.serialNumber}` : ""}
              </MenuItem>
            ))}
            <MenuItem value={ManualVolumePath}>Enter a path manually…</MenuItem>
          </TextField>
        )}
        {loadError && (
          <Feedback
            severity="warning"
            action={
              <Button onClick={() => setAttempt((value) => value + 1)} disabled={loading || submitting}>
                Retry
              </Button>
            }
          >
            {loadError}
          </Feedback>
        )}
        {!loading && !manual && candidates.length === 0 && !loadError && (
          <DialogContentText>
            {roots.length === 0 ? "No Volume discovery roots are configured (paths.volumes)." : `No mounted disk was found under ${roots.join(", ")}.`}
          </DialogContentText>
        )}
        {manual && (
          <>
            <TextField
              select
              margin="normal"
              label="Action"
              fullWidth
              disabled={fieldsDisabled}
              value={operation}
              onChange={(event) => {
                edited.current = true;
                setOperation(event.target.value as "initialize" | "register");
              }}
            >
              <MenuItem value="initialize">Initialize New</MenuItem>
              <MenuItem value="register">Register Existing</MenuItem>
            </TextField>
            <DialogContentText>
              {operation === "initialize"
                ? "Create a new identity marker and Library Media for an already mounted disk."
                : "Read an existing marker and recreate only its Library Media. Files and marker remain unchanged."}
            </DialogContentText>
          </>
        )}
        <TextField
          required
          margin="normal"
          label="Mount Point"
          fullWidth
          value={mountPoint}
          disabled={fieldsDisabled}
          onChange={(event) => {
            edited.current = true;
            setMountPoint(event.target.value);
          }}
        />
        <TextField
          required
          margin="normal"
          label="Name"
          fullWidth
          value={name}
          disabled={fieldsDisabled}
          onChange={(event) => {
            edited.current = true;
            setName(event.target.value);
          }}
        />
        {operation === "initialize" && (
          <>
            <TextField
              margin="normal"
              label="Serial Number"
              fullWidth
              value={serialNumber}
              disabled={fieldsDisabled}
              onChange={(event) => {
                edited.current = true;
                setSerialNumber(event.target.value);
              }}
            />
            <TextField
              select
              required
              margin="normal"
              label="Volume Type"
              fullWidth
              disabled={fieldsDisabled}
              value={type}
              onChange={(event: ChangeEvent<HTMLInputElement>) => {
                edited.current = true;
                setType(Number(event.target.value) as VolumeType);
              }}
            >
              <MenuItem value={VolumeType.HDD}>HDD · concurrent random read/write</MenuItem>
              <MenuItem value={VolumeType.HM_SMR}>HM-SMR · concurrent random read, sequential write</MenuItem>
            </TextField>
          </>
        )}
        {candidate && (
          <Alert severity={blocked ? "warning" : "info"}>
            {volumeCandidateNotice(candidate)}
            {!candidate.separateFilesystem ? " This directory is not a separately mounted filesystem." : ""}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button disabled={fieldsDisabled || blocked || !mountPoint.trim() || !name.trim()} onClick={() => runUIAction(submit, "Add Volume failed")}>
          {operation === "initialize" ? "Initialize" : "Register"}
        </Button>
        <Button onClick={close} disabled={submitting}>
          Cancel
        </Button>
      </DialogActions>
    </Dialog>
  );
};

const volumeStateLabel = (candidate: VolumeCandidate): string => {
  switch (candidate.state) {
    case VolumeCandidateState.UNREGISTERED:
      return "existing marker";
    case VolumeCandidateState.CONFLICT:
      return "marker conflict";
    case VolumeCandidateState.INVALID:
      return "invalid marker";
    default:
      return "new disk";
  }
};

export const InspectMediaDialog = ({ media, onClose }: { media: MediaFileData; onClose: () => void }) => {
  const { reply, loading, error, inspect } = useMediaInspect();
  const [devices, setDevices] = useState<string[] | null>(null);
  const [device, setDevice] = useState("");
  const titleId = useId();
  const theme = useTheme();
  const fullScreen = useMediaQuery(theme.breakpoints.down("sm"));

  useEffect(() => {
    if (media.media.kind === MediaKind.VOLUME) {
      runUIAction(() => inspect({ oneofKind: "volume", volume: { uuid: media.media.identity ?? "" } }), "Inspect Volume failed");
      return;
    }
    let active = true;
    runUIAction(async () => {
      const reply = await mediaCli.listDevices({}).response;
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
  }, [inspect, media.media]);

  const selectDevice = (value: string) => {
    setDevice(value);
    runUIAction(() => inspect({ oneofKind: "tape", tape: { device: value } }), "Inspect Tape failed");
  };

  return (
    <DetailDialog open onClose={onClose} maxWidth="sm" fullWidth fullScreen={fullScreen} aria-labelledby={titleId}>
      <DetailSurface title={`${media.name} · Properties`} titleId={titleId} icon={<StorageRoundedIcon fontSize="small" />} onClose={onClose}>
        {media.media.kind === MediaKind.TAPE && (
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
      </DetailSurface>
    </DetailDialog>
  );
};

export const MediaBrowser = () => {
  const navigate = useNavigate();
  const [addOpen, setAddOpen] = useState(false);
  const [inspectTarget, setInspectTarget] = useState<MediaFileData | null>(null);
  const [selection, setSelection] = useState<MediaFileData | MediaPositionFileData | null>(null);
  const [jobsMedia, setJobsMedia] = useState<MediaFileData>();
  const addVolume = useCallback(() => setAddOpen(true), []);
  const scanMedia = useCallback((media: MediaFileData) => navigate(`/scan?media=${media.id}`), [navigate]);
  const inspectMedia = useCallback((media: MediaFileData) => setInspectTarget(media), []);
  const selectFile = useCallback((row: MediaFileData | MediaPositionFileData | null) => setSelection(row), []);
  const verifyMedia = useCallback((media: MediaFileData) => navigate(`/scan?media=${media.id}&result=verify`), [navigate]);
  const {
    browserProps,
    listProps,
    refresh,
    medium,
    dialog: mediaDialog,
  } = useMediaBrowser(addVolume, scanMedia, inspectMedia, selectFile, verifyMedia, setJobsMedia);

  return (
    <Fragment>
      {mediaDialog}
      <Box className="browser-box">
        <Grid className="browser-container" container columnSpacing={1.5}>
          <Grid component="section" aria-label="Media browser" className="browser" size={7}>
            <FileBrowser {...browserProps} clearSelectionOnOutsideClick={false}>
              <FileNavbar />
              <FileToolbar />
              <FileList {...listProps} />
              <FileContextMenu />
            </FileBrowser>
          </Grid>
          <Grid className="browser" size={5}>
            <MediaInspector selection={selection} media={medium} />
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
    </Fragment>
  );
};
import { Feedback } from "@/components/feedback";
