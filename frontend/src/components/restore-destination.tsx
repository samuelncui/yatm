import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, ListItemText, MenuItem, TextField } from "@mui/material";
import { UnfoldMore } from "@mui/icons-material";
import { locationCli, settingsCli } from "@/api";
import { FileOperationKind, FileOperationRef, FileOperationSpec, OnlineBinding, type Location } from "@/entity";
import { DirectoryPicker } from "@/components/directory-picker";
import { useActionDialog } from "@/components/action-dialog";
import { useFileOperations } from "@/components/file-operations";
import { errorMessage } from "@/tools";

export type RestoreTarget = { location: Location; path: string };
const storageKey = "restore:last-target";
type StoredTarget = { locationID: string; rootPath: string; bindingToken: string; path: string };

function storedTarget(): StoredTarget | undefined {
  try {
    const value = JSON.parse(localStorage.getItem(storageKey) ?? "null") as StoredTarget | null;
    if (
      value &&
      /^[1-9]\d*$/.test(value.locationID) &&
      typeof value.rootPath === "string" &&
      typeof value.path === "string" &&
      typeof value.bindingToken === "string" &&
      value.bindingToken
    ) {
      return value;
    }
  } catch {
    // An unavailable or obsolete browser preference does not select a target.
  }
}

export const RestoreDestinationPicker = ({
  value,
  onChange,
  disabled,
}: {
  value?: RestoreTarget;
  onChange: (target: RestoreTarget) => void;
  disabled: boolean;
}) => {
  const [open, setOpen] = useState(false);
  const [notice, setNotice] = useState("");
  const restoreRequest = useRef(0);
  useEffect(() => {
    const stored = storedTarget();
    if (!stored) return;
    const pending = restoreRequest;
    const request = ++pending.current;
    void locationCli
      .get({ id: BigInt(stored.locationID), revision: 0n })
      .response.then(async (reply) => {
        if (request !== pending.current) return;
        const location = reply.location;
        if (
          !location?.restoreTarget ||
          location.binding !== OnlineBinding.CONFIRMED ||
          location.bindingToken !== stored.bindingToken ||
          location.rootPath !== stored.rootPath
        ) {
          setNotice("The last restore target is no longer available. Choose another target.");
          return;
        }
        await settingsCli.browsePaths({ locationId: location.id, path: stored.path, cursor: "", limit: 1 }).response;
        if (request === pending.current) onChange({ location, path: stored.path });
      })
      .catch(() => {
        if (request === pending.current) setNotice("Could not load the last restore target. Choose a target.");
      });
    return () => {
      pending.current++;
    };
  }, [onChange]);

  const choose = (target: RestoreTarget) => {
    onChange(target);
    setNotice("");
    setOpen(false);
    try {
      localStorage.setItem(
        storageKey,
        JSON.stringify({
          locationID: String(target.location.id),
          rootPath: target.location.rootPath,
          bindingToken: target.location.bindingToken,
          path: target.path,
        }),
      );
    } catch {
      setNotice("This browser could not remember the restore target.");
    }
  };
  return (
    <>
      <div className="restore-target-summary">
        <span>Restore to</span>
        <Button
          variant="outlined"
          endIcon={<UnfoldMore />}
          aria-label="Choose restore target"
          title={value && [value.location.rootPath, value.path].filter(Boolean).join("/")}
          disabled={disabled}
          onClick={() => {
            restoreRequest.current++;
            setOpen(true);
          }}
        >
          {value ? [value.location.name, value.path].filter(Boolean).join(" / ") : "Choose folder…"}
        </Button>
      </div>
      {notice && <Alert severity="info">{notice}</Alert>}
      {open && <RestoreDestinationDialog initial={value} onChoose={choose} onClose={() => setOpen(false)} />}
    </>
  );
};

const RestoreDestinationDialog = ({
  initial,
  onChoose,
  onClose,
}: {
  initial?: RestoreTarget;
  onChoose: (target: RestoreTarget) => void;
  onClose: () => void;
}) => {
  const [locations, setLocations] = useState<Location[]>([]);
  const [location, setLocation] = useState<Location>();
  const [directory, setDirectory] = useState(initial?.path ?? "");
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const cursor = useRef(0n);
  const request = useRef(0);
  const inFlight = useRef(false);
  const action = useActionDialog();
  const onOperationComplete = useCallback(async () => {}, []);
  const operations = useFileOperations(onOperationComplete);
  const load = useCallback(async (afterId = 0n) => {
    if (inFlight.current) return;
    inFlight.current = true;
    const current = ++request.current;
    setLoading(true);
    try {
      const page = await locationCli.list({ afterId, limit: 50, restoreTarget: true, query: "" }).response;
      if (current !== request.current) return;
      setLocations((previous) => [...new Map([...(afterId ? previous : []), ...page.locations].map((item) => [item.id, item])).values()]);
      cursor.current = page.locations.at(-1)?.id ?? afterId;
      setMore(page.hasMore);
      setError("");
    } catch (error) {
      if (current === request.current) setError(errorMessage(error, "Could not load restore destinations"));
    } finally {
      if (current === request.current) {
        inFlight.current = false;
        setLoading(false);
      }
    }
  }, []);
  useEffect(() => {
    const pending = request;
    const loading = inFlight;
    let active = true;
    void load();
    // A remembered target can be beyond the first page; refresh its eligibility independently.
    if (initial) {
      void locationCli
        .get({ id: initial.location.id, revision: 0n })
        .response.then(({ location }) => {
          if (
            active &&
            location?.restoreTarget &&
            location.binding === OnlineBinding.CONFIRMED &&
            location.bindingToken === initial.location.bindingToken &&
            location.rootPath === initial.location.rootPath
          ) {
            setLocation((current) => current ?? location);
          }
        })
        .catch(() => {});
    }
    return () => {
      active = false;
      pending.current++;
      loading.current = false;
    };
  }, [initial, load]);
  const options = location && !locations.some((item) => item.id === location.id) ? [location, ...locations] : locations;
  const createDirectory = (path: string) => {
    if (!location) return;
    action.ask({
      title: "New folder",
      confirmLabel: "Create",
      input: { label: "Folder name" },
      onConfirm: async (name) => {
        if (!name || name === "." || name === ".." || /[/\\\0]/.test(name)) throw new Error("Enter one folder name.");
        const parent = await locationCli.getEntry({ locationId: location.id, path }).response;
        if (!parent.isDir || !parent.reference || parent.reference.bindingToken !== location.bindingToken)
          throw new Error("The destination changed. Choose it again.");
        await operations.start(
          FileOperationSpec.create({
            kind: FileOperationKind.MAKE_DIRECTORY,
            destination: FileOperationRef.create({ target: { oneofKind: "location", location: parent.reference } }),
            name,
          }),
        );
        setDirectory([path, name].filter(Boolean).join("/"));
      },
    });
  };
  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="sm" slotProps={{ paper: { "aria-label": "Choose restore target" } }}>
      <DialogTitle>Choose restore target</DialogTitle>
      <DialogContent dividers className="restore-target-dialog" sx={{ p: 2 }}>
        <div className="restore-target-location">
          <TextField
            select
            fullWidth
            size="small"
            label="Location"
            value={location ? String(location.id) : ""}
            disabled={loading && !options.length}
            helperText={loading && !options.length ? "Loading Locations…" : undefined}
            slotProps={{ select: { renderValue: () => location?.name ?? "" } }}
            onChange={(event) => {
              const selected = options.find((item) => String(item.id) === event.target.value);
              if (!selected) return;
              setLocation(selected);
              setDirectory("");
            }}
          >
            <MenuItem value="" disabled>
              Select a Location
            </MenuItem>
            {options.map((item) => (
              <MenuItem key={String(item.id)} value={String(item.id)} disabled={item.binding === OnlineBinding.UNCONFIRMED} title={item.rootPath}>
                <ListItemText primary={item.name} secondary={item.binding === OnlineBinding.UNCONFIRMED ? "Confirm imported path" : undefined} />
              </MenuItem>
            ))}
          </TextField>
          {more && (
            <Button disabled={loading} onClick={() => void load(cursor.current)}>
              More locations
            </Button>
          )}
        </div>
        {error && (
          <Alert severity="error">
            {error}
            <Button disabled={loading} onClick={() => void load(cursor.current)}>
              Retry destinations
            </Button>
          </Alert>
        )}
        {!loading && !error && !options.length && (
          <Alert severity="info">
            No preferred Locations. <Link to="/settings/locations">Set a restore destination in Settings</Link>.
          </Alert>
        )}
        {location && (
          <div className="restore-target-browser">
            <DirectoryPicker
              locationID={location.id}
              rootName={location.name}
              initialPath={directory}
              onNewDirectory={createDirectory}
              onClose={onClose}
              onChoose={(path) => onChoose({ location, path })}
            />
          </div>
        )}
      </DialogContent>
      {action.dialog}
      {!location && (
        <DialogActions>
          <Button onClick={onClose}>Cancel</Button>
        </DialogActions>
      )}
    </Dialog>
  );
};
