import { Feedback } from "@/components/feedback";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useBeforeUnload, useBlocker } from "react-router";
import { Alert, Button, Checkbox, Grid, Stack, TextField } from "@mui/material";
import { locationCli } from "@/api";
import { Location } from "@/entity";
import { SettingsField } from "@/components/settings-field";
import { SettingsSection } from "@/components/settings-page";
import { LocationPathPicker } from "@/components/location-path-picker";
import { ActionDialog } from "@/components/action-dialog";
import { ActionRow } from "@/components/action-row";
import { errorMessage } from "@/tools";

export const LocationForm = ({
  source,
  onSaved,
  onCancel,
  onDirtyChange,
  additionalActions,
}: {
  source?: Location;
  onSaved: (location: Location) => void;
  onCancel: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  additionalActions?: (busy: boolean) => { beforeCancel: ReactNode; afterCancel: ReactNode };
}) => {
  const initial = {
    name: source?.name ?? "",
    rootPath: source?.rootPath ?? "",
    ignore: source?.config?.ignore?.text ?? "",
    restoreTarget: source?.restoreTarget ?? false,
    useMmap: source?.config?.useMmap ?? false,
  };
  const [value, setValue] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [browsing, setBrowsing] = useState(false);
  const leave = useRef(false);
  const discardConfirmed = useRef(false);
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
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const location = Location.create({
        ...source,
        name: value.name,
        rootPath: value.rootPath,
        restoreTarget: value.restoreTarget,
        config: { ignore: { format: "gitignore", text: value.ignore }, useMmap: value.useMmap },
      });
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
  const cancel = () => {
    leave.current = true;
    onCancel();
  };
  const actions = additionalActions?.(busy);
  return (
    <>
      <Stack
        component="form"
        noValidate
        useFlexGap
        spacing={2}
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        <SettingsSection title="Location">
          <Grid container spacing={2}>
            <Grid size={{ xs: 12, lg: 4 }}>
              <TextField required fullWidth label="Name" value={value.name} onChange={(event) => setValue({ ...value, name: event.target.value })} />
            </Grid>
            <Grid size={{ xs: 12, lg: 8 }}>
              <Stack direction={{ xs: "column", sm: "row" }} sx={{ alignItems: { xs: "stretch", sm: "flex-start" }, gap: 1 }}>
                <TextField
                  required
                  fullWidth
                  label="Directory"
                  value={value.rootPath}
                  onChange={(event) => setValue({ ...value, rootPath: event.target.value })}
                  helperText="A directory on the computer running YATM, within the administrator's allowed roots."
                />
                <Button disabled={busy} onClick={() => setBrowsing(true)} sx={{ flexShrink: 0, mt: { sm: 1 } }}>
                  Browse directories
                </Button>
              </Stack>
            </Grid>
            <Grid size={12}>
              <SettingsField
                label="Preferred restore destination"
                control={<Checkbox checked={value.restoreTarget} onChange={(_, restoreTarget) => setValue({ ...value, restoreTarget })} />}
              />
            </Grid>
            <Grid size={12}>
              <TextField
                fullWidth
                label="Ignore"
                multiline
                minRows={5}
                value={value.ignore}
                onChange={(event) => setValue({ ...value, ignore: event.target.value })}
                placeholder={"# Gitignore rules\n*.tmp\ndownloads/"}
                slotProps={{ htmlInput: { style: { fontFamily: "ui-monospace, monospace" } } }}
                helperText="Gitignore syntax. Rules apply in order; folder .gitignore files are not loaded."
              />
            </Grid>
          </Grid>
        </SettingsSection>
        <SettingsSection title="Advanced" collapsible>
          <SettingsField
            label="Use mmap for content reads"
            description="Use memory mapping when Archive and Scan read original file content. Off by default."
            control={<Checkbox checked={value.useMmap} onChange={(_, useMmap) => setValue({ ...value, useMmap })} />}
          />
        </SettingsSection>
        {source && dirty && (
          <Alert severity="info">Changing the directory or Ignore invalidates old file observations. Existing Jobs cannot be redirected.</Alert>
        )}
        {error && <Feedback severity="error">{error}</Feedback>}
        <ActionRow>
          <Button type="submit" variant="contained" disabled={busy || !value.name.trim() || !value.rootPath.trim() || (!!source && !dirty)}>
            {busy ? "Saving…" : source ? "Save changes" : "Add location"}
          </Button>
          {actions?.beforeCancel}
          <Button disabled={busy} onClick={cancel}>
            Cancel
          </Button>
          {actions?.afterCancel}
        </ActionRow>
      </Stack>
      {browsing && (
        <LocationPathPicker
          initialPath={value.rootPath}
          onClose={() => setBrowsing(false)}
          onChoose={(rootPath) => {
            setValue((current) => ({ ...current, rootPath }));
            setBrowsing(false);
          }}
        />
      )}
      {blocker.state === "blocked" && (
        <ActionDialog
          title="Discard unsaved changes?"
          confirmLabel="Discard"
          cancelLabel="Keep editing"
          danger
          onConfirm={() => {
            blocker.proceed?.();
            discardConfirmed.current = true;
          }}
          onClose={() => {
            if (!discardConfirmed.current) blocker.reset?.();
            discardConfirmed.current = false;
          }}
        >
          Your Location configuration has not been saved.
        </ActionDialog>
      )}
    </>
  );
};
