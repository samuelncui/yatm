import { Feedback } from "@/components/feedback";
import { useEffect, useState } from "react";
import { Button, Checkbox, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, Stack, Typography } from "@mui/material";
import { toast } from "react-toastify";
import { filesCli } from "@/api";
import { errorMessage } from "@/tools";
import { readStored, removeStored, writeStored, stringCodec } from "@/state/storage";

/** Remembers that the operator accepted inventory admission without the count each time. */
export const importPositionsPreferenceKey = "files:import-positions-without-prompt";

export const admissionToast = (files: bigint, existing: bigint, skipped: bigint) => {
  const skippedRecords = [existing > 0n ? `${existing} already in Library` : "", skipped > 0n ? `${skipped} without signatures` : ""]
    .filter(Boolean)
    .join("; ");
  if (files > 0n) {
    toast.success(`Added ${files} Files to Library${skippedRecords ? `; skipped ${skippedRecords}` : ""}`);
    return;
  }
  toast.info(skippedRecords ? `No new Files to add: ${skippedRecords}.` : "No recorded files to add.");
};

/**
 * Admits one Media's recorded content as independent Library Files. The count comes from a dry
 * run, and the confirmed call repeats the same request to write. Once the operator asks not to
 * see the count again, a later click imports directly. Explicit review always performs a dry run.
 */
export const useLibraryAdmission = (mediaID: bigint | undefined) => {
  const [state, setState] = useState<{ phase: "idle" | "calculating" | "confirm" | "importing" } | { phase: "error"; message: string }>({ phase: "idle" });
  const [files, setFiles] = useState(0n);
  const [existing, setExisting] = useState(0n);
  const [skipped, setSkipped] = useState(0n);
  const [directories, setDirectories] = useState(0n);
  const [skipPrompt, setSkipPrompt] = useState(false);
  const [promptSuppressed, setPromptSuppressed] = useState(false);

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
      if (remember && !skipPrompt && promptSuppressed) removeStored("local", importPositionsPreferenceKey);
      admissionToast(reply.fileCount, reply.existingCount, reply.skippedFileCount);
      close();
    } catch (error) {
      setState({ phase: "error", message: errorMessage(error, "Could not add these records") });
    }
  };

  const review = () => {
    if (!mediaID || state.phase === "calculating" || state.phase === "importing") return;
    const suppressed = readStored("local", importPositionsPreferenceKey, stringCodec) === "1";
    setPromptSuppressed(suppressed);
    setSkipPrompt(suppressed);
    setState({ phase: "calculating" });
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
    promptSuppressed,
    restorePrompt: () => {
      if (!removeStored("local", importPositionsPreferenceKey)) {
        toast.info("Could not save this browser preference.");
        return;
      }
      setPromptSuppressed(false);
      setSkipPrompt(false);
    },
    review,
    ask: () => {
      if (!mediaID || state.phase === "calculating" || state.phase === "importing") return;
      if (readStored("local", importPositionsPreferenceKey, stringCodec) === "1") {
        void run(false);
        return;
      }
      review();
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
      <DialogTitle>Add existing inventory to Library</DialogTitle>
      <DialogContent>
        <Stack spacing={2}>
          {admission.calculating && (
            <Stack direction="row" spacing={1.5} sx={{ alignItems: "center" }}>
              <CircularProgress size={20} />
              <Typography>Calculating what this Media would add…</Typography>
            </Stack>
          )}
          {!admission.calculating && !admission.error && !admission.busy && (
            <>
              <Typography>
                {admission.counts.files > 0n
                  ? `${admission.counts.files} files${name ? ` from ${name}` : ""} will be added as independent Library files.`
                  : "No new Files will be added."}
              </Typography>
              <Summary counts={admission.counts} />
              <FormControlLabel
                control={
                  <Checkbox
                    disabled={admission.busy || !admission.canConfirm}
                    checked={admission.skipPrompt}
                    onChange={(_, next) => admission.setSkipPrompt(next)}
                  />
                }
                label="Skip this confirmation for all Media in this browser"
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
        {admission.promptSuppressed && (
          <Button type="button" disabled={admission.busy} onClick={admission.restorePrompt}>
            Show confirmation next time
          </Button>
        )}
        <Button type="button" disabled={admission.busy} onClick={admission.close}>
          Cancel
        </Button>
      </DialogActions>
    </Dialog>
  );
};

const Summary = ({ counts }: { counts: { files: bigint; directories: bigint; existing: bigint; skipped: bigint } }) => {
  const parts = [
    counts.directories > 0n ? `${counts.directories} folder entries` : "",
    counts.existing > 0n ? `${counts.existing} already in Library` : "",
    counts.skipped > 0n ? `${counts.skipped} without signatures` : "",
  ].filter(Boolean);
  if (!parts.length && counts.files > 0n) return null;
  return (
    <Typography variant="body2" color="text.secondary">
      {parts.length ? parts.join(" · ") : "No recorded files to add."}
    </Typography>
  );
};
