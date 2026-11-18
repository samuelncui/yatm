import { t } from "@lingui/core/macro";
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
import { ActionRow } from "@/components/action-row";
import { useSettingsEditor } from "@/components/settings-editor";
import { SettingsField } from "@/components/settings-field";
import { SettingsActions, SettingsEditorFeedback, SettingsSection } from "@/components/settings-page";
import { LibrarySettings, SettingsGroup } from "@/entity";
import { loadLocations } from "@/state/locations";
import { useAppStore } from "@/state/react";

export const SettingsBrowser = () => {
  const editor = useSettingsEditor(SettingsGroup.LIBRARY, LibrarySettings);
  const { cancel, dirty, draft, error, invalid, loading, reload, save, saved, saving, setDraft } = editor;
  return (
    <>
      <SettingsEditorFeedback
        loading={loading}
        saving={saving}
        error={error}
        invalid={invalid}
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
        {draft && (
          <SettingsSection title={t`File visibility`}>
            <SettingsField
              label={t`Include files without saved versions`}
              description={t`When off, Library shows files with saved versions. Locations are unaffected.`}
              control={
                <Checkbox
                  checked={draft.includeFilesWithoutVersions}
                  disabled={loading || saving}
                  onChange={(_, includeFilesWithoutVersions) => setDraft((value) => (value ? { ...value, includeFilesWithoutVersions } : value))}
                />
              }
            />
          </SettingsSection>
        )}
        <SettingsSection title={t`Library data`}>
          <Typography variant="body2" color="text.secondary">
            {t`Export includes the Library, Locations and original links. Import replaces these from the backup; global preferences stay unchanged. Disk files are not restored.`}
          </Typography>
          <ActionRow>
            <ImportLibraryButton />
            <Button variant="outlined" startIcon={<DownloadRoundedIcon />} onClick={() => window.location.assign(fileBase + "/library/_export")}>
              {t`Export`}
            </Button>
          </ActionRow>
        </SettingsSection>
        <SettingsActions dirty={dirty} loading={loading} saving={saving} onCancel={cancel} />
      </Stack>
    </>
  );
};

const ImportLibraryButton = () => {
  const store = useAppStore();
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
      if (!response.ok) throw new Error((await response.text()) || t`Import failed (${response.status})`);
      void loadLocations(store, true);
      setStatus("success");
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : t`Could not import the Library backup`);
      setStatus("error");
    }
  };

  return (
    <Fragment>
      <Button variant="outlined" startIcon={<UploadRoundedIcon />} onClick={() => setOpen(true)}>
        {t`Import`}
      </Button>
      {open && (
        <Dialog open onClose={status === "pending" ? undefined : close} maxWidth="sm" fullWidth>
          <DialogTitle>{t`Import Library Backup`}</DialogTitle>
          <DialogContent>
            <Stack useFlexGap spacing={2}>
              <Alert severity="info">
                {t`Import replaces the Library, including Locations and original links, from this backup. Global preferences stay unchanged. Disk files are not restored.`}
              </Alert>
              {status === "pending" && <LinearProgress aria-label={t`Importing Library backup`} />}
              {status === "success" && (
                <Alert severity="success">{t`Library, Locations and original links imported. Global preferences were not changed.`}</Alert>
              )}
              {status === "error" && <Feedback severity="error">{error}</Feedback>}
              <Button variant="outlined" component="label" startIcon={<UploadRoundedIcon />} disabled={status === "pending"}>
                {t`Select backup`}
                <input type="file" accept=".json,.jsonl,application/json,application/x-ndjson" onChange={selectFile} hidden />
              </Button>
              {file && <div>{file.name}</div>}
            </Stack>
          </DialogContent>
          <DialogActions>
            {status !== "success" && (
              <Button variant="contained" disabled={!file || status === "pending"} onClick={submit}>
                {status === "pending" ? t`Importing…` : t`Import`}
              </Button>
            )}
            <Button disabled={status === "pending"} onClick={close}>
              {status === "success" ? t`Close` : t`Cancel`}
            </Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};
