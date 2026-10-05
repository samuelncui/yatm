import { Feedback } from "@/components/feedback";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  Breadcrumbs,
  breadcrumbsClasses,
  Button,
  buttonClasses,
  CircularProgress,
  IconButton,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Tooltip,
} from "@mui/material";
import { ArrowUpward, ErrorOutlineOutlined, FolderOutlined } from "@mui/icons-material";
import { filesCli } from "@/api";
import { EntryKind, FileOperationKind, FileScope, FilesInclude, type FilesEntry } from "@/entity";
import { locationDirectoryReference } from "@/components/files-browser";
import { errorMessage } from "@/tools";
import { ActionRow } from "./action-row";

type DirectoryPickerProps = {
  locationID: bigint;
  initialPath?: string;
  rootName?: string;
  onNewDirectory?: (path: string) => void;
  onChoose: (path: string) => void;
  onClose: () => void;
};

export const DirectoryPicker = (props: DirectoryPickerProps) => <DirectoryPickerContents key={`${props.locationID}:${props.initialPath ?? ""}`} {...props} />;

// listLocationDirectories reads one live directory completely, so the picker shows the
// whole list at once instead of following a cursor.
export const listLocationDirectories = async (locationID: bigint, path: string, signal: AbortSignal) => {
  signal.throwIfAborted();
  const call = filesCli.list(
    {
      directory: locationDirectoryReference(String(locationID), path),
      scope: FileScope.ALL,
      batchSize: 500,
      include: [FilesInclude.NAVIGATION, FilesInclude.OPERATIONS],
    },
    { abort: signal },
  );
  const entries: FilesEntry[] = [];
  let breadcrumbs: FilesEntry[] = [];
  let directory: FilesEntry | undefined;
  for await (const batch of call.responses) {
    signal.throwIfAborted();
    entries.push(...batch.entries);
    if (batch.breadcrumbs.length) breadcrumbs = batch.breadcrumbs;
    directory = batch.directory ?? directory;
  }
  signal.throwIfAborted();
  return { entries, breadcrumbs, directory };
};

export function canChooseLocationDirectory(entry: FilesEntry | undefined, locationID: bigint, path: string) {
  const target = entry?.reference?.target;
  return (
    !entry?.error &&
    entry?.kind === EntryKind.DIRECTORY &&
    target?.oneofKind === "location" &&
    target.location.locationId === locationID &&
    target.location.path === path &&
    entry.operations.includes(FileOperationKind.MKDIR)
  );
}

type DirectoryRow = { name: string; path: string; error?: string };
const DirectoryPickerContents = ({ locationID, initialPath = "", rootName = "/", onNewDirectory, onChoose, onClose }: DirectoryPickerProps) => {
  const [path, setPath] = useState(initialPath);
  const [rows, setRows] = useState<DirectoryRow[]>([]);
  const [breadcrumbs, setBreadcrumbs] = useState<DirectoryRow[]>([]);
  const [writable, setWritable] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const request = useRef<AbortController | null>(null);
  // One live directory read returns every entry, so the picker selects the directories itself and
  // shows the whole list instead of following a cursor.
  const load = useCallback(async () => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    try {
      const page = await listLocationDirectories(locationID, path, controller.signal);
      if (controller.signal.aborted) return;
      setRows(page.entries.filter((entry) => entry.kind === EntryKind.DIRECTORY || entry.error));
      setBreadcrumbs(page.breadcrumbs.filter((entry) => entry.path));
      setWritable(canChooseLocationDirectory(page.directory, locationID, path));
      setError("");
    } catch (error) {
      if (!controller.signal.aborted) {
        setRows([]);
        setError(errorMessage(error, "Could not browse directory"));
      }
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }, [path, locationID]);
  useEffect(() => {
    const pending = request;
    void load();
    return () => pending.current?.abort();
  }, [load]);
  const navigate = (nextPath: string) => {
    if (nextPath === path) return;
    request.current?.abort();
    setPath(nextPath);
    setRows([]);
    setBreadcrumbs([]);
    setWritable(false);
    setError("");
    setLoading(true);
  };
  const descend = (row: DirectoryRow) => {
    if (!row.error) navigate(row.path);
  };
  const pathButtons = breadcrumbs;
  return (
    <section className="directory-picker" aria-label="Choose directory">
      <div className="directory-picker-path">
        <Tooltip title="Parent directory">
          <span>
            <IconButton size="small" aria-label="Parent directory" disabled={loading || !path} onClick={() => navigate(path.split("/").slice(0, -1).join("/"))}>
              <ArrowUpward fontSize="small" />
            </IconButton>
          </span>
        </Tooltip>
        <Breadcrumbs
          maxItems={4}
          aria-label="Destination path"
          sx={{
            flex: 1,
            minWidth: 0,
            [`& .${breadcrumbsClasses.ol}`]: { flexWrap: "nowrap" },
            [`& .${breadcrumbsClasses.li}`]: { minWidth: 0 },
            [`& .${buttonClasses.root}`]: {
              minWidth: 0,
              p: "2px 4px",
              fontSize: 13,
              maxWidth: 180,
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
              display: "block",
            },
          }}
        >
          <Button size="small" disabled={loading} aria-current={!path ? "page" : undefined} onClick={() => navigate("")}>
            {rootName}
          </Button>
          {pathButtons.map((part) => (
            <Button key={part.path} size="small" disabled={loading} aria-current={part.path === path ? "page" : undefined} onClick={() => navigate(part.path)}>
              {part.name}
            </Button>
          ))}
        </Breadcrumbs>
        {loading && <CircularProgress size={16} aria-label="Loading directories" />}
      </div>
      {error && (
        <Feedback
          severity="error"
          action={
            <Button disabled={loading} onClick={() => void load()}>
              Retry
            </Button>
          }
        >
          {error}
        </Feedback>
      )}
      {!loading && !error && !writable && <Alert severity="info">This directory cannot be used as a restore destination.</Alert>}
      <List dense aria-label="Directories">
        {rows.map((row, index) => (
          <ListItemButton
            key={row.error ? `unavailable:${index}` : row.path}
            disabled={loading || !!row.error}
            onDoubleClick={() => descend(row)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                descend(row);
              }
            }}
          >
            <ListItemIcon sx={{ minWidth: 32 }}>{row.error ? <ErrorOutlineOutlined color="error" /> : <FolderOutlined />}</ListItemIcon>
            <ListItemText primary={row.name} secondary={row.error} />
          </ListItemButton>
        ))}
      </List>
      <ActionRow className="directory-picker-actions">
        <Button variant="contained" disabled={loading || !!error || !writable} onClick={() => onChoose(path)}>
          Choose
        </Button>
        {onNewDirectory && (
          <Button disabled={loading || !!error || !writable} onClick={() => onNewDirectory(path)}>
            New folder
          </Button>
        )}
        <Button
          onClick={() => {
            request.current?.abort();
            onClose();
          }}
        >
          Cancel
        </Button>
      </ActionRow>
    </section>
  );
};
