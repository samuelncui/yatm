import { Feedback } from "@/components/feedback";
import { useEffect, useRef, useState } from "react";
import { Alert, Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, LinearProgress, Stack } from "@mui/material";
import type { FileData } from "@samuelncui/chonky";
import { restoreJobCli } from "@/api";
import { EstimateRestoreJobRequest, type FileVersion } from "@/entity";
import { errorMessage, formatFilesize } from "@/tools";
import { contentTime } from "@/components/content-status";
import { PreviewMedia } from "@/components/file-preview";
import { SavedVersionChoice } from "@/components/saved-version-choice";
import { useContentPreview } from "./use-content-preview";
import { useFileVersionPages } from "./use-file-versions";
export const ChooseVersionDialog = ({
  file,
  selectedVersion,
  cutoff,
  allowDamagedCopies = false,
  onClose,
  onChoose,
}: {
  file: FileData;
  selectedVersion?: FileVersion;
  cutoff?: bigint;
  allowDamagedCopies?: boolean;
  onClose: () => void;
  onChoose: (version: FileVersion) => Promise<void>;
}) => {
  const selectedVersionID = selectedVersion?.id;
  const { versions, more, loading, failure, load } = useFileVersionPages(BigInt(file.id), selectedVersionID);
  const [selectedID, setSelectedID] = useState(selectedVersionID);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    setSelectedID(selectedVersionID);
    setError("");
    return () => {
      mounted.current = false;
    };
  }, [file.id, selectedVersionID]);
  const loadError = failure === undefined ? "" : errorMessage(failure, "Load versions failed");
  const choices = selectedVersion && !versions.some((version) => version.id === selectedVersion.id) ? [selectedVersion, ...versions] : versions;
  const selected = choices.find((version) => version.id === selectedID);
  const choose = async () => {
    if (!selected || saving) return;
    setSaving(true);
    setError("");
    try {
      await onChoose(selected);
      if (mounted.current) onClose();
    } catch (error) {
      if (mounted.current) setError(errorMessage(error, "Select version failed"));
    } finally {
      if (mounted.current) setSaving(false);
    }
  };
  return (
    <Dialog open onClose={() => !saving && onClose()} maxWidth="sm" fullWidth>
      <DialogTitle>Choose a version of {file.name}</DialogTitle>
      <DialogContent dividers>
        {loading && <LinearProgress />}
        {(error || loadError) && (
          <Feedback
            severity="error"
            action={
              <Button
                disabled={loading}
                onClick={() => {
                  setError("");
                  void load();
                }}
              >
                Retry
              </Button>
            }
          >
            {error || loadError}
          </Feedback>
        )}
        {!loading && !error && !loadError && choices.length === 0 && <p>No saved versions.</p>}
        <Stack spacing={1}>
          {choices.map((version) => (
            <SavedVersionChoice key={String(version.id)} selected={version.id === selectedID} disabled={saving} onClick={() => setSelectedID(version.id)}>
              <span>
                {contentTime(version.lastArchivedAtNs ?? version.firstArchivedAtNs, "Archive date unknown")} · {formatFilesize(version.sizeBytes)}
                <small>
                  Version #{String(version.id)}
                  {version.id === selectedVersionID ? " · Current selection" : ""}
                </small>
              </span>
            </SavedVersionChoice>
          ))}
        </Stack>
        {more && (
          <Button disabled={loading || saving} onClick={() => void load(versions.at(-1)?.id)}>
            More versions
          </Button>
        )}
        {selected && <VersionDetails key={String(selected.id)} version={selected} cutoff={cutoff} allowDamagedCopies={allowDamagedCopies} />}
      </DialogContent>
      <DialogActions>
        <Button variant="contained" disabled={!selected || saving} onClick={() => void choose()}>
          Choose version
        </Button>
        <Button disabled={saving} onClick={onClose}>
          Cancel
        </Button>
      </DialogActions>
    </Dialog>
  );
};

const VersionDetails = ({ version, cutoff, allowDamagedCopies }: { version: FileVersion; cutoff?: bigint; allowDamagedCopies: boolean }) => {
  const { assets: preview, error: previewError, reload: reloadPreview } = useContentPreview(version.signature);
  const [unavailable, setUnavailable] = useState<boolean>();
  const [availabilityError, setAvailabilityError] = useState("");
  const [availabilityAttempt, setAvailabilityAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    setUnavailable(undefined);
    setAvailabilityError("");
    void restoreJobCli
      .estimate(EstimateRestoreJobRequest.create({ fileVersionIds: [version.id], allowDamagedCopies }))
      .response.then((availability) => {
        if (!active) return;
        if (!availability.result) throw new Error("Selection estimate is missing");
        setUnavailable(availability.result.missingCopyCount > 0n);
      })
      .catch((error) => {
        if (active) setAvailabilityError(errorMessage(error, "Could not check this version"));
      });
    return () => {
      active = false;
    };
  }, [version.id, allowDamagedCopies, availabilityAttempt]);
  const afterCutoff = cutoff !== undefined && version.firstArchivedAtNs !== undefined && version.firstArchivedAtNs > cutoff;
  return (
    <Stack spacing={1} sx={{ mt: 2 }}>
      {!!preview?.length && (
        <Box sx={{ "& img": { maxWidth: "100%", maxHeight: 180, objectFit: "contain" } }}>
          <PreviewMedia key={JSON.stringify(preview)} assets={preview} />
        </Box>
      )}
      {afterCutoff ? <Alert severity="info">This custom version was saved after the selected time.</Alert> : null}
      {unavailable && <Alert severity="warning">No usable archived copy.</Alert>}
      {previewError && (
        <Feedback severity="info" action={<Button onClick={reloadPreview}>Retry Preview</Button>}>
          {previewError}
        </Feedback>
      )}
      {availabilityError && (
        <Feedback severity="warning" action={<Button onClick={() => setAvailabilityAttempt((value) => value + 1)}>Retry availability</Button>}>
          {availabilityError}
        </Feedback>
      )}
    </Stack>
  );
};
