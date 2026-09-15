import { useCallback, useEffect, useRef, useState } from "react";
import { useBeforeUnload, useBlocker } from "react-router";
import { Alert, Button, Checkbox, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, Stack, TextField } from "@mui/material";
import { locationCli, settingsCli } from "@/api";
import { Location } from "@/entity";
import { DirectoryPicker } from "@/components/directory-picker";
import { errorMessage } from "@/tools";

export const LocationForm = ({
  source,
  required = [],
  onSaved,
  onCancel,
  onDirtyChange,
}: {
  source?: Location;
  required?: string[];
  onSaved: (location: Location) => void;
  onCancel: () => void;
  onDirtyChange?: (dirty: boolean) => void;
}) => {
  const initial = {
    name: source?.name ?? "",
    rootPath: source?.rootPath ?? "",
    ignore: source?.ignore?.text ?? "",
    restoreTarget: source?.restoreTarget ?? false,
    writeTrackingUuid: source?.writeTrackingUuid ?? false,
  };
  const [value, setValue] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [browsing, setBrowsing] = useState(false);
  const [autoCollect, setAutoCollect] = useState<boolean>();
  const [settingsAttempt, setSettingsAttempt] = useState(0);
  useEffect(() => {
    if (source) return;
    let active = true;
    void settingsCli
      .getLibrary({})
      .response.then((settings) => {
        if (active) {
          setAutoCollect(settings.autoCollectFiles);
          setError("");
        }
      })
      .catch((error) => {
        if (active) setError(errorMessage(error, "Could not load collection settings"));
      });
    return () => {
      active = false;
    };
  }, [source, settingsAttempt]);
  const leave = useRef(false);
  const dirty = JSON.stringify(value) !== JSON.stringify(initial);
  useEffect(() => {
    onDirtyChange?.(dirty);
    return () => onDirtyChange?.(false);
  }, [dirty, onDirtyChange]);
  const blocker = useBlocker(() => dirty && !leave.current);
  useBeforeUnload(
    useCallback(
      (event: BeforeUnloadEvent) => {
        if (dirty && !leave.current) {
          event.preventDefault();
          event.returnValue = "";
        }
      },
      [dirty],
    ),
  );
  const save = async () => {
    if (busy || (!source && autoCollect === undefined)) return;
    setBusy(true);
    setError("");
    try {
      const location = Location.create({ ...source, ...value, ignore: { format: "gitignore", text: value.ignore } });
      const reply = await (source ? locationCli.update({ location }) : locationCli.create({ location })).response;
      if (!reply.location) throw new Error("Location reply is missing");
      leave.current = true;
      onSaved(reply.location);
    } catch (error) {
      setError(errorMessage(error, "Could not save location"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <form
        className="location-config-form"
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        <Stack spacing={2}>
          <TextField required label="Name" value={value.name} onChange={(event) => setValue({ ...value, name: event.target.value })} />
          <TextField
            required
            label="Directory"
            value={value.rootPath}
            onChange={(event) => setValue({ ...value, rootPath: event.target.value })}
            helperText="A directory on the computer running YATM, within the administrator's allowed roots."
          />
          <Button sx={{ alignSelf: "flex-start" }} onClick={() => setBrowsing(!browsing)}>
            Browse directories
          </Button>
          {browsing && (
            <DirectoryPicker
              initialPath={value.rootPath}
              onClose={() => setBrowsing(false)}
              onChoose={(rootPath) => {
                setValue({ ...value, rootPath });
                setBrowsing(false);
              }}
            />
          )}
          <FormControlLabel
            control={<Checkbox checked={value.restoreTarget} onChange={(_, restoreTarget) => setValue({ ...value, restoreTarget })} />}
            label="Preferred restore destination"
          />
          <TextField
            label="Ignore"
            multiline
            minRows={5}
            value={value.ignore}
            onChange={(event) => setValue({ ...value, ignore: event.target.value })}
            placeholder={"# Gitignore rules\n*.tmp\ndownloads/"}
            slotProps={{ htmlInput: { style: { fontFamily: "ui-monospace, monospace" } } }}
            helperText="Gitignore syntax. Rules apply in order; folder .gitignore files are not loaded."
          />
          <details>
            <summary>Advanced tracking</summary>
            <FormControlLabel
              control={<Checkbox checked={value.writeTrackingUuid} onChange={(_, writeTrackingUuid) => setValue({ ...value, writeTrackingUuid })} />}
              label="Write a tracking UUID when missing"
            />
            <p className="product-muted">Adds an optional extended attribute. Existing values are never overwritten.</p>
          </details>
          {required.length > 0 && (
            <details>
              <summary>Protected paths</summary>
              <ul>
                {required.map((path) => (
                  <li key={path}>
                    <code>{path}</code>
                  </li>
                ))}
              </ul>
            </details>
          )}
          {source && dirty && (
            <Alert severity="info">Changing the directory or Ignore invalidates old file observations. Existing Jobs cannot be redirected.</Alert>
          )}
          {!source && autoCollect && <p className="product-muted">Basic file information will be added to Library in a background Job.</p>}
          {error && (
            <Alert severity="error">
              {error}
              {!source && autoCollect === undefined && <Button onClick={() => setSettingsAttempt((value) => value + 1)}>Retry</Button>}
            </Alert>
          )}
          <div className="product-actions">
            <Button
              disabled={busy}
              onClick={() => {
                leave.current = true;
                onCancel();
              }}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              variant="contained"
              disabled={busy || (!source && autoCollect === undefined) || !value.name.trim() || !value.rootPath.trim() || (!!source && !dirty)}
            >
              {busy ? "Saving…" : source ? "Save changes" : "Add location"}
            </Button>
          </div>
        </Stack>
      </form>
      <Dialog open={blocker.state === "blocked"} onClose={() => blocker.reset?.()}>
        <DialogTitle>Discard unsaved changes?</DialogTitle>
        <DialogContent>Your Location configuration has not been saved.</DialogContent>
        <DialogActions>
          <Button onClick={() => blocker.reset?.()}>Keep editing</Button>
          <Button color="error" onClick={() => blocker.proceed?.()}>
            Discard
          </Button>
        </DialogActions>
      </Dialog>
    </>
  );
};
