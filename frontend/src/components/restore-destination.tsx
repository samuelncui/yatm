import { t } from "@lingui/core/macro";
import { Trans } from "@lingui/react/macro";
import { Feedback } from "@/components/feedback";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, ListItemText, MenuItem, TextField } from "@mui/material";
import { UnfoldMore } from "@mui/icons-material";
import { filesCli, locationCli } from "@/api";
import { FileOperationKind, FileOperationSpec, type Location, type RestoreDestination } from "@/entity";
import { locationDirectoryReference } from "@/components/files-browser";
import { canChooseLocationDirectory, DirectoryPicker, listLocationDirectories } from "@/components/directory-picker";
import { useActionDialog } from "@/components/action-dialog";
import { useFileOperations } from "@/components/file-operations";
import { useLocationChoices } from "./use-location-choices";

export type RestoreTarget = { location: Location; path: string };
export const RestoreDestinationPicker = ({
  value,
  onChange,
  disabled,
  last,
}: {
  value?: RestoreTarget;
  onChange: (target: RestoreTarget) => void;
  disabled: boolean;
  /** The destination of the most recent Restore, offered when it is still a valid target. */
  last?: RestoreDestination;
}) => {
  const [open, setOpen] = useState(false);
  const [notice, setNotice] = useState("");
  const restoreRequest = useRef<AbortController | null>(null);
  useEffect(() => {
    if (!last || last.locationId <= 0n || value) return;
    const controller = new AbortController();
    restoreRequest.current = controller;
    void locationCli
      .get({ id: last.locationId }, { abort: controller.signal })
      .response.then(async (reply) => {
        if (controller.signal.aborted) return;
        const location = reply.location;
        if (!location?.restoreTarget || location.rootPath !== last.rootPath) {
          setNotice(t`The last restore target is no longer available. Choose another target.`);
          return;
        }
        const page = await listLocationDirectories(location.id, last.path, controller.signal);
        if (controller.signal.aborted) return;
        if (!canChooseLocationDirectory(page.directory)) {
          setNotice(t`The last restore target is no longer available. Choose another target.`);
          return;
        }
        onChange({ location, path: last.path });
      })
      .catch(() => {
        if (!controller.signal.aborted) setNotice(t`Could not load the last restore target. Choose a target.`);
      });
    return () => controller.abort();
  }, [onChange, last, value]);

  const choose = (target: RestoreTarget) => {
    onChange(target);
    setNotice("");
    setOpen(false);
  };
  return (
    <>
      <div className="restore-target-summary">
        <span>{t`Restore to`}</span>
        <Button
          variant="outlined"
          endIcon={<UnfoldMore />}
          aria-label={t`Choose restore target`}
          title={value && [value.location.rootPath, value.path].filter(Boolean).join("/")}
          disabled={disabled}
          onClick={() => {
            restoreRequest.current?.abort();
            setOpen(true);
          }}
        >
          {value ? [value.location.name, value.path].filter(Boolean).join(" / ") : t`Choose folder…`}
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
  const { locations, loading, error, retry } = useLocationChoices({ enabled: true, restoreTarget: true });
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
      title: t`New folder`,
      confirmLabel: t`Create`,
      input: { label: t`Folder name` },
      onConfirm: async (name) => {
        if (!name || name === "." || name === ".." || /[/\0]/.test(name)) throw new Error(t`Enter one folder name.`);
        const parent = (await filesCli.get({ reference: locationDirectoryReference(String(location.id), path) }).response).detail?.entry;
        if (!canChooseLocationDirectory(parent)) throw new Error(t`The destination changed. Choose it again.`);
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
    <Dialog open onClose={onClose} fullWidth maxWidth="sm" slotProps={{ paper: { "aria-label": t`Choose restore target` } }}>
      <DialogTitle>{t`Choose restore target`}</DialogTitle>
      <DialogContent dividers className="restore-target-dialog" sx={{ p: 2 }}>
        <div className="restore-target-location">
          <TextField
            select
            fullWidth
            size="small"
            label={t`Location`}
            value={location ? String(location.id) : ""}
            disabled={loading && !options.length}
            helperText={loading && !options.length ? t`Loading Locations…` : undefined}
            slotProps={{ select: { renderValue: () => location?.name ?? "" } }}
            onChange={(event) => {
              const selected = options.find((item) => String(item.id) === event.target.value);
              if (!selected) return;
              setLocation(selected);
              setDirectory("");
            }}
          >
            <MenuItem value="" disabled>
              {t`Select a Location`}
            </MenuItem>
            {options.map((item) => (
              <MenuItem key={String(item.id)} value={String(item.id)} title={item.rootPath}>
                <ListItemText primary={item.name} />
              </MenuItem>
            ))}
          </TextField>
        </div>
        {error && (
          <Feedback
            severity="error"
            action={
              <Button disabled={loading} onClick={retry}>
                {t`Retry destinations`}
              </Button>
            }
          >
            {error}
          </Feedback>
        )}
        {!loading && !error && !options.length && (
          <Alert severity="info">
            <Trans>
              No preferred Locations. <Link to="/settings/locations">Set a restore destination in Settings</Link>.
            </Trans>
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
          <Button onClick={onClose}>{t`Cancel`}</Button>
        </DialogActions>
      )}
    </Dialog>
  );
};
