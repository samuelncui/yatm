import { useEffect, useState } from "react";
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogTitle, MenuItem, Stack, TextField } from "@mui/material";
import { fileCatalogCli, locationCli } from "@/api";
import { Location, OnlineBinding, type File, type LocationEntryRef } from "@/entity";
import { LocationEntryBrowser } from "@/components/location-entry-browser";
import { errorMessage } from "@/tools";

export const RelocateOriginalDialog = ({ file, onClose, onSaved }: { file: File; onClose: () => void; onSaved: () => Promise<void> }) => {
  const [locations, setLocations] = useState<Location[]>([]);
  const [more, setMore] = useState(false);
  const [location, setLocation] = useState<Location>();
  const [reference, setReference] = useState<LocationEntryRef>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const load = async (afterId = 0n) => {
    setLoading(true);
    try {
      const reply = await locationCli.list({ afterId, limit: 50, query: "" }).response;
      setLocations((current) => (afterId ? [...current, ...reply.locations] : reply.locations));
      setMore(reply.hasMore);
      setError("");
    } catch (error) {
      setError(errorMessage(error, "Could not load Locations"));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, []);
  const save = async () => {
    if (!reference || busy) return;
    setBusy(true);
    setError("");
    try {
      await fileCatalogCli.relocateOriginal({ fileId: file.id, reference }).response;
      await onSaved();
      onClose();
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
            disabled={busy}
            onChange={(event) => {
              setReference(undefined);
              setLocation(locations.find((value) => value.id.toString() === event.target.value));
            }}
          >
            {locations.map((value) => (
              <MenuItem key={String(value.id)} value={String(value.id)} disabled={value.binding === OnlineBinding.UNCONFIRMED}>
                {value.name}
              </MenuItem>
            ))}
          </TextField>
          {more && (
            <Button disabled={loading} onClick={() => void load(locations.at(-1)?.id)}>
              More Locations
            </Button>
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
          {error && (
            <Alert severity="error">
              {error}
              {!location && (
                <Button disabled={loading} onClick={() => void load()}>
                  Retry
                </Button>
              )}
            </Alert>
          )}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        {reference && (
          <Button disabled={busy} onClick={() => setReference(undefined)}>
            Choose another file
          </Button>
        )}
        <Button variant="contained" disabled={!reference || busy} onClick={() => void save()}>
          Link original
        </Button>
      </DialogActions>
    </Dialog>
  );
};
