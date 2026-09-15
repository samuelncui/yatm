import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, LinearProgress, Stack } from "@mui/material";
import type { FileData } from "@samuelncui/chonky";
import { fileCatalogCli } from "@/api";
import { InspectSelectionRequest, type FileVersion, type PreviewManifest } from "@/entity";
import { errorMessage, formatFilesize } from "@/tools";
import { contentTime } from "@/components/content-status";
import { PreviewMedia } from "@/components/file-preview";
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
  const [versions, setVersions] = useState<FileVersion[]>([]);
  const selectedVersionID = selectedVersion?.id;
  const [selectedID, setSelectedID] = useState(selectedVersionID);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const request = useRef(0);
  const mounted = useRef(true);
  const load = useCallback(
    async (afterId = 0n) => {
      const sequence = ++request.current;
      setLoading(true);
      try {
        const reply = await fileCatalogCli.listVersions({ fileId: BigInt(file.id), afterId, limit: 20 }).response;
        if (sequence !== request.current) return;
        setVersions((current) => (afterId ? [...current, ...reply.versions] : reply.versions));
        setMore(reply.hasMore);
        setError("");
      } catch (error) {
        if (sequence === request.current) setError(errorMessage(error, "Load versions failed"));
      } finally {
        if (sequence === request.current) setLoading(false);
      }
    },
    [file.id],
  );
  useEffect(() => {
    const pending = request;
    mounted.current = true;
    setVersions([]);
    setSelectedID(selectedVersionID);
    void load();
    return () => {
      pending.current++;
      mounted.current = false;
    };
  }, [load, selectedVersionID]);
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
        {error && (
          <Alert
            severity="error"
            action={
              <Button disabled={loading} onClick={() => void load()}>
                Retry
              </Button>
            }
          >
            {error}
          </Alert>
        )}
        {!loading && !error && choices.length === 0 && <p>No saved versions.</p>}
        <Stack spacing={1}>
          {choices.map((version) => (
            <Button
              key={String(version.id)}
              variant={version.id === selectedID ? "outlined" : "text"}
              aria-pressed={version.id === selectedID}
              sx={{ justifyContent: "flex-start", textAlign: "left", textTransform: "none" }}
              disabled={saving}
              onClick={() => setSelectedID(version.id)}
            >
              <span>
                {contentTime(version.lastArchivedAtMs ?? version.firstArchivedAtMs, "Backup date unknown")} · {formatFilesize(version.size)}
                <small className="version-choice-details">
                  Version #{String(version.id)}
                  {version.id === selectedVersionID ? " · Current selection" : ""}
                </small>
              </span>
            </Button>
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
        <Button disabled={saving} onClick={onClose}>
          Cancel
        </Button>
        <Button variant="contained" disabled={!selected || saving} onClick={() => void choose()}>
          Choose version
        </Button>
      </DialogActions>
    </Dialog>
  );
};

const VersionDetails = ({ version, cutoff, allowDamagedCopies }: { version: FileVersion; cutoff?: bigint; allowDamagedCopies: boolean }) => {
  const [preview, setPreview] = useState<PreviewManifest>();
  const [unavailable, setUnavailable] = useState<boolean>();
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    setPreview(undefined);
    setUnavailable(undefined);
    setError("");
    void Promise.all([
      fileCatalogCli.getVersion({ id: version.id }).response,
      fileCatalogCli.inspectSelection(InspectSelectionRequest.create({ fileVersionIds: [version.id], restore: true, allowDamagedCopies })).response,
    ])
      .then(([detail, availability]) => {
        if (!active) return;
        setPreview(detail.preview);
        setUnavailable(availability.missingCopies > 0n);
      })
      .catch((error) => {
        if (active) setError(errorMessage(error, "Could not check this version"));
      });
    return () => {
      active = false;
    };
  }, [version.id, allowDamagedCopies]);
  const afterCutoff = cutoff !== undefined && version.firstArchivedAtMs !== undefined && version.firstArchivedAtMs > cutoff;
  return (
    <Stack spacing={1} sx={{ mt: 2 }}>
      {preview && (
        <Box sx={{ "& img": { maxWidth: "100%", maxHeight: 180, objectFit: "contain" } }}>
          <PreviewMedia fileID={version.fileId} versionID={version.id} manifest={preview} />
        </Box>
      )}
      {afterCutoff ? <Alert severity="info">This custom version was saved after the selected time.</Alert> : null}
      {unavailable && <Alert severity="warning">No usable archived copy.</Alert>}
      {error && <Alert severity="warning">{error}</Alert>}
    </Stack>
  );
};
