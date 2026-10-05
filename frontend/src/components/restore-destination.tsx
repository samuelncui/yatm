import { Feedback } from "@/components/feedback";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, ListItemText, MenuItem, TextField } from "@mui/material";
import { UnfoldMore } from "@mui/icons-material";
import { filesCli, locationCli } from "@/api";
import { FileOperationKind, FileOperationSpec, type Location } from "@/entity";
import { locationDirectoryReference } from "@/components/files-browser";
import { canChooseLocationDirectory, DirectoryPicker, listLocationDirectories } from "@/components/directory-picker";
import { useActionDialog } from "@/components/action-dialog";
import { useFileOperations } from "@/components/file-operations";
import { readStored, writeStored, type StorageCodec } from "@/state/storage";
import { useLocationChoices } from "./use-location-choices";

export type RestoreTarget = { location: Location; path: string };
const storageKey = "restore:last-target";
type StoredTarget = { locationID: string; rootPath: string; path: string };

const targetCodec: StorageCodec<StoredTarget> = {
  encode: (value) => JSON.stringify(value),
  decode: (raw) => {
    const value = JSON.parse(raw) as StoredTarget | null;
    if (
      value &&
      typeof value.locationID === "string" &&
      /^[1-9]\d*$/.test(value.locationID) &&
      typeof value.rootPath === "string" &&
      typeof value.path === "string"
    ) {
      return value;
    }
    throw new Error("Invalid stored restore target");
  },
};

export const RestoreDestinationPicker = ({
  value,
  onChange,
  disabled,
  usePreference = true,
}: {
  value?: RestoreTarget;
  onChange: (target: RestoreTarget) => void;
  disabled: boolean;
  usePreference?: boolean;
}) => {
  const [open, setOpen] = useState(false);
  const [notice, setNotice] = useState("");
  const restoreRequest = useRef<AbortController | null>(null);
  useEffect(() => {
    if (!usePreference || value) return;
    const stored = readStored("local", storageKey, targetCodec);
    if (!stored) return;
    const controller = new AbortController();
    restoreRequest.current = controller;
    void locationCli
      .get({ id: BigInt(stored.locationID) }, { abort: controller.signal })
      .response.then(async (reply) => {
        if (controller.signal.aborted) return;
        const location = reply.location;
        if (!location?.restoreTarget || location.rootPath !== stored.rootPath) {
          setNotice("The last restore target is no longer available. Choose another target.");
          return;
        }
        const page = await listLocationDirectories(location.id, stored.path, controller.signal);
        if (controller.signal.aborted) return;
        if (!canChooseLocationDirectory(page.directory, location.id, stored.path)) {
          setNotice("The last restore target is no longer available. Choose another target.");
          return;
        }
        onChange({ location, path: stored.path });
      })
      .catch(() => {
        if (!controller.signal.aborted) setNotice("Could not load the last restore target. Choose a target.");
      });
    return () => controller.abort();
  }, [onChange, usePreference, value]);

  const choose = (target: RestoreTarget) => {
    onChange(target);
    setNotice("");
    setOpen(false);
    if (!writeStored("local", storageKey, { locationID: String(target.location.id), rootPath: target.location.rootPath, path: target.path }, targetCodec)) {
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
            restoreRequest.current?.abort();
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
  const { locations, more, loading, error, loadMore, retry } = useLocationChoices({ enabled: true, restoreTarget: true });
  const [location, setLocation] = useState<Location>();
  const [directory, setDirectory] = useState(initial?.path ?? "");
  const action = useActionDialog();
  const onOperationComplete = useCallback(async () => {}, []);
  const operations = useFileOperations(onOperationComplete);
  useEffect(() => {
    const controller = new AbortController();
    // A remembered target can be beyond the first page; refresh its eligibility independently.
    if (initial) {
      void locationCli
        .get({ id: initial.location.id }, { abort: controller.signal })
        .response.then(({ location }) => {
          if (!controller.signal.aborted && location?.restoreTarget && location.rootPath === initial.location.rootPath) {
            setLocation((current) => current ?? location);
          }
        })
        .catch(() => {});
    }
    return () => controller.abort();
  }, [initial]);
  const options = location && !locations.some((item) => item.id === location.id) ? [location, ...locations] : locations;
  const createDirectory = (path: string) => {
    if (!location) return;
    action.ask({
      title: "New folder",
      confirmLabel: "Create",
      input: { label: "Folder name" },
      onConfirm: async (name) => {
        if (!name || name === "." || name === ".." || /[/\0]/.test(name)) throw new Error("Enter one folder name.");
        const parent = (await filesCli.get({ reference: locationDirectoryReference(String(location.id), path) }).response).detail?.entry;
        if (!canChooseLocationDirectory(parent, location.id, path)) throw new Error("The destination changed. Choose it again.");
        await operations.start(
          FileOperationSpec.create({
            kind: FileOperationKind.MKDIR,
            destination: parent?.reference,
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
              <MenuItem key={String(item.id)} value={String(item.id)} title={item.rootPath}>
                <ListItemText primary={item.name} />
              </MenuItem>
            ))}
          </TextField>
          {more && !error && (
            <Button disabled={loading} onClick={loadMore}>
              More locations
            </Button>
          )}
        </div>
        {error && (
          <Feedback
            severity="error"
            action={
              <Button disabled={loading} onClick={retry}>
                Retry destinations
              </Button>
            }
          >
            {error}
          </Feedback>
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
