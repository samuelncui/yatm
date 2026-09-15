import { ChangeEvent, Fragment, useEffect, useState } from "react";
import { Link } from "react-router";
import { Alert, Checkbox, FormControlLabel } from "@mui/material";

import BackupTableRoundedIcon from "@mui/icons-material/BackupTableRounded";
import DownloadRoundedIcon from "@mui/icons-material/DownloadRounded";
import UploadRoundedIcon from "@mui/icons-material/UploadRounded";
import Button from "@mui/material/Button";
import Card from "@mui/material/Card";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";

import { fileBase, settingsCli } from "@/api";
import type { LibrarySettings } from "@/entity";
import { errorMessage } from "@/tools";
import { useActionDialog } from "@/components/action-dialog";

export const SettingsBrowser = () => (
  <div className="settings-page">
    <header className="settings-header">
      <h1>Library</h1>
    </header>
    <LibraryDisplaySettings />
    <Card className="settings-card">
      <div className="settings-section">
        <div className="settings-section-icon">
          <BackupTableRoundedIcon />
        </div>
        <div className="settings-section-copy">
          <strong>Library data</strong>
        </div>
        <div className="settings-section-actions">
          <Button variant="outlined" startIcon={<DownloadRoundedIcon />} onClick={() => window.location.assign(fileBase + "/library/_export")}>
            Export
          </Button>
          <ImportLibraryButton />
        </div>
      </div>
    </Card>
  </div>
);

const LibraryDisplaySettings = () => {
  const [settings, setSettings] = useState<LibrarySettings>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [collectionJobs, setCollectionJobs] = useState<bigint[]>([]);
  const { ask, dialog } = useActionDialog();
  useEffect(() => {
    let active = true;
    void settingsCli
      .getLibrary({})
      .response.then((value) => {
        if (active) setSettings(value);
      })
      .catch((error) => {
        if (active) setError(errorMessage(error, "Could not load Library settings"));
      });
    return () => {
      active = false;
    };
  }, []);
  const save = async (changes: Partial<LibrarySettings>) => {
    if (!settings || saving) return;
    setSaving(true);
    try {
      const reply = await settingsCli.updateLibrary({ settings: { ...settings, ...changes } }).response;
      setSettings(reply.settings);
      setCollectionJobs(reply.collectionJobIds);
      setError(reply.collectionErrors.join("; "));
    } catch (error) {
      setError(errorMessage(error, "Could not save Library settings"));
    } finally {
      setSaving(false);
    }
  };
  return (
    <Card className="settings-card">
      <div className="settings-section">
        <div className="settings-section-copy">
          <strong>Location files</strong>
          <FormControlLabel
            label="Automatically add Location files to Library"
            control={
              <Checkbox
                checked={settings?.autoCollectFiles ?? true}
                disabled={!settings || saving}
                onChange={(_, checked) => {
                  if (!checked) {
                    void save({ autoCollectFiles: false });
                    return;
                  }
                  ask({
                    title: "Add files from existing Locations?",
                    confirmLabel: "Enable",
                    children: <p>Basic information will be collected in background Jobs. File contents are not analyzed.</p>,
                    onConfirm: () => save({ autoCollectFiles: true }),
                  });
                }}
              />
            }
          />
          <strong>File visibility</strong>
          <FormControlLabel
            label="Include unbacked files"
            control={
              <Checkbox
                checked={settings?.includeUnbackedFiles ?? true}
                disabled={!settings || saving}
                onChange={(_, checked) => void save({ includeUnbackedFiles: checked })}
              />
            }
          />
          <span>When off, Library shows files with saved versions. Locations are unaffected.</span>
          <strong>Disk operations</strong>
          <FormControlLabel
            label="Confirm before permanent deletion"
            control={
              <Checkbox
                checked={settings?.confirmPermanentDelete ?? true}
                disabled={!settings || saving}
                onChange={(_, checked) => void save({ confirmPermanentDelete: checked })}
              />
            }
          />
          {!settings?.confirmPermanentDelete && settings && <span>Deleting in a Location immediately removes the selected disk files.</span>}
          {collectionJobs.map((id) => (
            <Link key={String(id)} to={`/jobs/${id}`}>
              Collection Job {String(id)}
            </Link>
          ))}
          {error && <Alert severity="error">{error}</Alert>}
        </div>
      </div>
      {dialog}
    </Card>
  );
};

const ImportLibraryButton = () => {
  const [open, setOpen] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const close = () => {
    setOpen(false);
    setFile(null);
  };
  const selectFile = (event: ChangeEvent<HTMLInputElement>) => {
    setFile(event.target.files?.[0] ?? null);
  };
  const submit = async () => {
    if (!file) return;

    const response = await fetch(fileBase + "/library/_import", { body: file, method: "POST" });
    console.log(await response.json());
    close();
  };

  return (
    <Fragment>
      <Button variant="contained" startIcon={<UploadRoundedIcon />} onClick={() => setOpen(true)}>
        Import
      </Button>
      {open && (
        <Dialog open onClose={close} maxWidth="sm" fullWidth>
          <DialogTitle>Import Library Backup</DialogTitle>
          <DialogContent>
            <Button variant="outlined" component="label" startIcon={<UploadRoundedIcon />}>
              Select backup
              <input type="file" accept=".json,.jsonl,application/json,application/x-ndjson" onChange={selectFile} hidden />
            </Button>
            {file && <p>{file.name}</p>}
          </DialogContent>
          <DialogActions>
            <Button onClick={close}>Cancel</Button>
            <Button variant="contained" disabled={!file} onClick={submit}>
              Import
            </Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};
