import { Feedback } from "@/components/feedback";
import { ChangeEvent, Fragment, useState } from "react";
import { Alert, Checkbox, LinearProgress, Stack, Typography } from "@mui/material";

import DownloadRoundedIcon from "@mui/icons-material/DownloadRounded";
import UploadRoundedIcon from "@mui/icons-material/UploadRounded";
import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";

import { fileBase } from "@/api";
import { useSettingsEditor } from "@/components/settings-editor";
import { SettingsField } from "@/components/settings-field";
import { SettingsActions, SettingsEditorFeedback, SettingsPage, SettingsSection } from "@/components/settings-page";
import { LibrarySettings, SettingsGroup } from "@/entity";

export const SettingsBrowser = () => (
  <SettingsPage title="Library" description="What Library shows, how it removes items, and where its data can go.">
    <LibraryDisplaySettings />
  </SettingsPage>
);

const LibraryDisplaySettings = () => {
  const editor = useSettingsEditor(SettingsGroup.LIBRARY, LibrarySettings, "Library");
  const { cancel, dirty, draft, error, loading, reload, save, saved, saving, setDraft } = editor;
  return (
    <>
      <SettingsEditorFeedback
        label="Library"
        loading={loading}
        saving={saving}
        error={error}
        saved={saved}
        dirty={dirty}
        hasDraft={!!draft}
        onReload={reload}
      />
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
        <SettingsSection title="File visibility">
          <SettingsField
            label="Include unbacked files"
            description="When off, Library shows files with saved versions. Locations are unaffected."
            control={
              <Checkbox
                checked={draft?.includeUnbackedFiles ?? false}
                disabled={!draft || loading || saving}
                onChange={(_, includeUnbackedFiles) => setDraft((value) => (value ? { ...value, includeUnbackedFiles } : value))}
              />
            }
          />
        </SettingsSection>
        <SettingsSection title="File operations">
          <SettingsField
            label="Confirm Delete"
            description={draft && !draft.confirmRemove ? "Delete moves selected items to Trash immediately." : undefined}
            control={
              <Checkbox
                checked={draft?.confirmRemove ?? false}
                disabled={!draft || loading || saving}
                onChange={(_, confirmRemove) => setDraft((value) => (value ? { ...value, confirmRemove } : value))}
              />
            }
          />
        </SettingsSection>
        <SettingsSection title="Library data">
          <Typography variant="body2" color="text.secondary">
            Export writes the whole Library; import replaces it from that backup. Settings are not exported or changed.
          </Typography>
        </SettingsSection>
        <SettingsActions dirty={dirty} loading={loading} saving={saving} onCancel={cancel}>
          <LibraryDataActions />
        </SettingsActions>
      </Stack>
    </>
  );
};

const LibraryDataActions = () => (
  <>
    <ImportLibraryButton />
    <Button variant="outlined" startIcon={<DownloadRoundedIcon />} onClick={() => window.location.assign(fileBase + "/library/_export")}>
      Export
    </Button>
  </>
);

const ImportLibraryButton = () => {
  const [open, setOpen] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [status, setStatus] = useState<"idle" | "pending" | "success" | "error">("idle");
  const [error, setError] = useState("");
  const close = () => {
    setOpen(false);
    setFile(null);
    setStatus("idle");
    setError("");
  };
  const selectFile = (event: ChangeEvent<HTMLInputElement>) => {
    setFile(event.target.files?.[0] ?? null);
    setStatus("idle");
    setError("");
  };
  const submit = async () => {
    if (!file || status === "pending") return;
    setStatus("pending");
    setError("");
    try {
      const response = await fetch(fileBase + "/library/_import", { body: file, method: "POST" });
      if (!response.ok) throw new Error((await response.text()) || `Import failed (${response.status})`);
      setStatus("success");
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not import the Library backup");
      setStatus("error");
    }
  };

  return (
    <Fragment>
      <Button variant="outlined" startIcon={<UploadRoundedIcon />} onClick={() => setOpen(true)}>
        Import
      </Button>
      {open && (
        <Dialog open onClose={status === "pending" ? undefined : close} maxWidth="sm" fullWidth>
          <DialogTitle>Import Library Backup</DialogTitle>
          <DialogContent>
            <Stack useFlexGap spacing={2}>
              <Alert severity="info">Import replaces Library data. Your Settings stay unchanged.</Alert>
              {status === "pending" && <LinearProgress aria-label="Importing Library backup" />}
              {status === "success" && <Alert severity="success">Library imported. Settings were not changed.</Alert>}
              {status === "error" && <Feedback severity="error">{error}</Feedback>}
              <Button variant="outlined" component="label" startIcon={<UploadRoundedIcon />} disabled={status === "pending"}>
                Select backup
                <input type="file" accept=".json,.jsonl,application/json,application/x-ndjson" onChange={selectFile} hidden />
              </Button>
              {file && <div>{file.name}</div>}
            </Stack>
          </DialogContent>
          <DialogActions>
            {status !== "success" && (
              <Button variant="contained" disabled={!file || status === "pending"} onClick={submit}>
                {status === "pending" ? "Importing…" : "Import"}
              </Button>
            )}
            <Button disabled={status === "pending"} onClick={close}>
              {status === "success" ? "Close" : "Cancel"}
            </Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};
