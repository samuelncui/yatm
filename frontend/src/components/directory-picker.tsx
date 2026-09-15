import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Breadcrumbs, Button, CircularProgress, IconButton, List, ListItemButton, ListItemIcon, ListItemText, Tooltip } from "@mui/material";
import { ArrowUpward, FolderOutlined } from "@mui/icons-material";
import { settingsCli } from "@/api";
import type { SourceFile } from "@/entity";
import { errorMessage } from "@/tools";

type DirectoryPickerProps = {
  locationID?: bigint;
  initialPath?: string;
  rootName?: string;
  onNewDirectory?: (path: string) => void;
  onChoose: (path: string) => void;
  onClose: () => void;
};

export const DirectoryPicker = (props: DirectoryPickerProps) => (
  <DirectoryPickerContents key={`${props.locationID ?? 0n}:${props.initialPath ?? ""}`} {...props} />
);

const DirectoryPickerContents = ({ locationID = 0n, initialPath = "", rootName = "/", onNewDirectory, onChoose, onClose }: DirectoryPickerProps) => {
  const [path, setPath] = useState(initialPath);
  const [rows, setRows] = useState<SourceFile[]>([]);
  const [cursor, setCursor] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const sequence = useRef(0);
  const load = useCallback(
    async (cursor = "") => {
      const request = ++sequence.current;
      setLoading(true);
      try {
        const page = await settingsCli.browsePaths({ path, locationId: locationID, cursor, limit: 100 }).response;
        if (request !== sequence.current) return;
        setRows((current) => (cursor ? [...current, ...page.directories] : page.directories));
        setCursor(page.nextCursor);
        setError("");
      } catch (error) {
        if (request === sequence.current) {
          setRows([]);
          setCursor("");
          setError(errorMessage(error, "Could not browse directory"));
        }
      } finally {
        if (request === sequence.current) setLoading(false);
      }
    },
    [path, locationID],
  );
  useEffect(() => {
    const pending = sequence;
    void load();
    return () => {
      pending.current++;
    };
  }, [load]);
  const navigate = (nextPath: string) => {
    if (nextPath === path) return;
    sequence.current++;
    setPath(nextPath);
    setRows([]);
    setCursor("");
    setError("");
    setLoading(true);
  };
  const descend = (row: SourceFile) => navigate(locationID ? [path, row.name].filter(Boolean).join("/") : row.path);
  return (
    <section className="directory-picker" aria-label="Choose directory">
      <div className="directory-picker-path">
        <Tooltip title={locationID ? "Parent directory" : "Allowed roots"}>
          <span>
            <IconButton
              size="small"
              aria-label={locationID ? "Parent directory" : "Allowed roots"}
              disabled={loading || !path}
              onClick={() => navigate(locationID ? path.split("/").slice(0, -1).join("/") : "")}
            >
              <ArrowUpward fontSize="small" />
            </IconButton>
          </span>
        </Tooltip>
        <Breadcrumbs maxItems={4} aria-label="Destination path">
          <Button size="small" disabled={loading} aria-current={!path ? "page" : undefined} onClick={() => navigate("")}>
            {locationID ? rootName : "Allowed directories"}
          </Button>
          {path
            .split("/")
            .filter(Boolean)
            .map((name, index, parts) => (
              <Button
                key={parts.slice(0, index + 1).join("/")}
                size="small"
                disabled={loading}
                aria-current={index === parts.length - 1 ? "page" : undefined}
                onClick={() => navigate((!locationID && path.startsWith("/") ? "/" : "") + parts.slice(0, index + 1).join("/"))}
              >
                {name}
              </Button>
            ))}
        </Breadcrumbs>
        {loading && <CircularProgress size={16} aria-label="Loading directories" />}
      </div>
      {error && (
        <Alert severity="error">
          {error}
          <Button disabled={loading} onClick={() => void load()}>
            Retry
          </Button>
        </Alert>
      )}
      <List dense aria-label="Directories">
        {rows.map((row) => (
          <ListItemButton
            key={row.path}
            disabled={loading}
            onDoubleClick={() => descend(row)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                descend(row);
              }
            }}
          >
            <ListItemIcon>
              <FolderOutlined />
            </ListItemIcon>
            <ListItemText primary={row.name} />
          </ListItemButton>
        ))}
      </List>
      {cursor && (
        <Button disabled={loading} onClick={() => void load(cursor)}>
          Load more
        </Button>
      )}
      <div className="directory-picker-actions">
        {onNewDirectory && (
          <Button disabled={loading || !!error} onClick={() => onNewDirectory(path)}>
            New folder
          </Button>
        )}
        <span />
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" disabled={loading || !!error || (!locationID && !path)} onClick={() => onChoose(path)}>
          Choose
        </Button>
      </div>
    </section>
  );
};
