import { Feedback } from "@/components/feedback";
import { useEffect, useState } from "react";
import { Button, Checkbox, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, Stack, Typography } from "@mui/material";
import { toast } from "react-toastify";
import { filesCli } from "@/api";
import { errorMessage } from "@/tools";
import { readStored, writeStored, stringCodec } from "@/state/storage";

/** Remembers that the operator accepted inventory admission without the count each time. */
export const importPositionsPreferenceKey = "files:import-positions-without-prompt";

const admissionToast = (files: bigint, existing: bigint, skipped: bigint) => {
  if (files > 0n) {
    const kept = existing > 0n ? `; skipped ${existing} already in Library` : "";
    const unsigned = skipped > 0n ? `; skipped ${skipped} without signatures` : "";
    toast.success(`Added ${files} Files to Library${kept}${unsigned}`);
    return;
  }
  toast.info(existing > 0n ? "These records are already in Library." : "This Media has no recorded content to add.");
};

/**
 * Admits one Media's recorded content as independent Library Files. The count comes from a dry
 * run, and the confirmed call repeats the same request to write. Once the operator asks not to
 * see the count again, a later click imports directly.
 */
export const useLibraryAdmission = (mediaID: bigint | undefined) => {
  const [state, setState] = useState<{ phase: "idle" | "calculating" | "confirm" | "importing" } | { phase: "error"; message: string }>({ phase: "idle" });
  const [files, setFiles] = useState(0n);
  const [existing, setExisting] = useState(0n);
  const [skipped, setSkipped] = useState(0n);
  const [directories, setDirectories] = useState(0n);
  const [skipPrompt, setSkipPrompt] = useState(false);

  // The dry run answers one question per open dialog; a stale response cannot reopen it.
  useEffect(() => {
    if (state.phase !== "calculating" || !mediaID) return;
    const controller = new AbortController();
    let active = true;
    void filesCli
      .importPositions({ positionIds: [], mediaId: mediaID, dryrun: true }, { abort: controller.signal })
      .response.then((reply) => {
        if (!active) return;
        setFiles(reply.fileCount);
        setDirectories(reply.directoryCount);
        setExisting(reply.existingCount);
        setSkipped(reply.skippedFileCount);
        setState({ phase: "confirm" });
      })
      .catch((error: unknown) => {
        if (!active || controller.signal.aborted) return;
        setState({ phase: "error", message: errorMessage(error, "Could not inspect this Media") });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [state.phase, mediaID]);

  const close = () => {
    setState({ phase: "idle" });
    setSkipPrompt(false);
  };

  const run = async (remember: boolean) => {
    if (!mediaID) return;
    setState({ phase: "importing" });
    try {
      const reply = await filesCli.importPositions({ positionIds: [], mediaId: mediaID, dryrun: false }).response;
      if (remember && skipPrompt) writeStored("local", importPositionsPreferenceKey, "1", stringCodec);
      admissionToast(reply.fileCount, reply.existingCount, reply.skippedFileCount);
      close();
    } catch (error) {
      setState({ phase: "error", message: errorMessage(error, "Could not add these records") });
    }
  };

  return {
    open: state.phase !== "idle",
    calculating: state.phase === "calculating",
    canConfirm: state.phase === "confirm" && files > 0n,
    busy: state.phase === "importing",
    error: state.phase === "error" ? state.message : "",
    counts: { files, directories, existing, skipped },
    skipPrompt,
    setSkipPrompt,
    ask: () => {
      if (readStored("local", importPositionsPreferenceKey, stringCodec) === "1") {
        void run(false);
        return;
      }
      setState({ phase: "calculating" });
    },
    confirm: () => void run(true),
    retry: () => setState({ phase: "calculating" }),
    close,
  };
};

export const ImportPositionsDialog = ({ admission, name }: { admission: ReturnType<typeof useLibraryAdmission>; name?: string }) => {
  if (!admission.open) return null;
  return (
    <Dialog open onClose={() => (admission.busy ? undefined : admission.close())} maxWidth="sm" fullWidth>
      <DialogTitle>Add to Library</DialogTitle>
      <DialogContent>
        <Stack spacing={2}>
          {admission.calculating && (
            <Stack direction="row" spacing={1.5} sx={{ alignItems: "center" }}>
              <CircularProgress size={20} />
              <Typography>Calculating what this Media would add…</Typography>
            </Stack>
          )}
          {!admission.calculating && !admission.error && (
            <>
              <Typography>
                {admission.counts.files > 0n
                  ? `${admission.counts.files} files${name ? ` from ${name}` : ""} will be added as independent Library files.`
                  : "This Media has no new recorded content to add."}
              </Typography>
              <Summary counts={admission.counts} />
              <FormControlLabel
                control={<Checkbox checked={admission.skipPrompt} onChange={(_, next) => admission.setSkipPrompt(next)} />}
                label="Don't show this next time"
              />
            </>
          )}
          {admission.error && <Feedback severity="error">{admission.error}</Feedback>}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button type="button" variant="contained" disabled={admission.busy || !admission.canConfirm} onClick={admission.confirm}>
          {admission.busy ? "Adding…" : "Add to Library"}
        </Button>
        {admission.error && (
          <Button type="button" onClick={admission.retry}>
            Retry
          </Button>
        )}
        <Button type="button" disabled={admission.busy} onClick={admission.close}>
          Cancel
        </Button>
      </DialogActions>
    </Dialog>
  );
};

const Summary = ({ counts }: { counts: { directories: bigint; existing: bigint; skipped: bigint } }) => {
  const parts = [
    counts.directories > 0n ? `${counts.directories} folder entries` : "",
    counts.existing > 0n ? `${counts.existing} already in Library` : "",
    counts.skipped > 0n ? `${counts.skipped} without signatures` : "",
  ].filter(Boolean);
  if (!parts.length) return null;
  return (
    <Typography variant="body2" color="text.secondary">
      {parts.join(" · ")}
    </Typography>
  );
};
