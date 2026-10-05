import { Feedback } from "@/components/feedback";
import { useState } from "react";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, MenuItem, Stack, TextField } from "@mui/material";
import { filesCli } from "@/api";
import type { Location, File, LocationEntryRef } from "@/entity";
import { LocationEntryBrowser } from "@/components/location-entry-browser";
import { errorMessage, runUIAction } from "@/tools";
import { useLocationChoices } from "./use-location-choices";

export const RelocateOriginalDialog = ({ file, onClose, onSaved }: { file: Pick<File, "id" | "name">; onClose: () => void; onSaved: () => Promise<void> }) => {
  const { locations, more, loading, error: locationError, loadMore, retry } = useLocationChoices({ enabled: true });
  const [location, setLocation] = useState<Location>();
  const [reference, setReference] = useState<LocationEntryRef>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const save = async () => {
    if (!reference || busy) return;
    setBusy(true);
    setError("");
    try {
      await filesCli.relocateOriginal({ fileId: file.id, reference, dryrun: false }).response;
      onClose();
      runUIAction(onSaved, "Original linked, but its details could not be refreshed");
    } catch (error) {
      setError(errorMessage(error, "Could not link this original"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open onClose={busy ? undefined : onClose} fullWidth maxWidth="md">
      <DialogTitle>Locate original · {file.name}</DialogTitle>
      <DialogContent>
        <Stack spacing={2}>
          <TextField
            select
            label="Location"
            value={location?.id.toString() ?? ""}
            disabled={busy || (loading && !locations.length)}
            helperText={loading && !locations.length ? "Loading Locations…" : undefined}
            onChange={(event) => {
              setError("");
              setReference(undefined);
              setLocation(locations.find((value) => value.id.toString() === event.target.value));
            }}
          >
            {locations.map((value) => (
              <MenuItem key={String(value.id)} value={String(value.id)}>
                {value.name}
              </MenuItem>
            ))}
          </TextField>
          {more && !locationError && (
            <Button disabled={busy || loading} onClick={loadMore}>
              More Locations
            </Button>
          )}
          {locationError && (
            <Feedback
              severity="error"
              action={
                <Button disabled={busy || loading} onClick={retry}>
                  Retry Locations
                </Button>
              }
            >
              {locationError}
            </Feedback>
          )}
          {location && !reference && <LocationEntryBrowser key={String(location.id)} source={location} onChoose={setReference} />}
          {reference && (
            <Alert severity="warning">
              Link <strong>{file.name}</strong> to{" "}
              <strong>
                {location?.name}/{reference.path}
              </strong>
              ? This replaces its original reference without moving files or changing tags, notes, or saved versions.
            </Alert>
          )}
          {error && <Feedback severity="error">{error}</Feedback>}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button variant="contained" disabled={!reference || busy} onClick={() => void save()}>
          Link original
        </Button>
        {reference && (
          <Button
            disabled={busy}
            onClick={() => {
              setError("");
              setReference(undefined);
            }}
          >
            Choose another file
          </Button>
        )}
        <Button disabled={busy} onClick={onClose}>
          Cancel
        </Button>
      </DialogActions>
    </Dialog>
  );
};
