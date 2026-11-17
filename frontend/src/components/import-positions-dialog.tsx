import { t } from "@lingui/core/macro";
import { Feedback } from "@/components/feedback";
import { useEffect, useState } from "react";
import { Button, Checkbox, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, Stack, Typography } from "@mui/material";
import { toast } from "react-toastify";
import { filesCli } from "@/api";
import { errorMessage } from "@/tools";
import { useSkippableConfirmation } from "./confirmations";

export const admissionToast = (files: bigint, existing: bigint, skipped: bigint) => {
  const existingCount = String(existing);
  const skippedCount = String(skipped);
  const recordParts = [existing > 0n ? t`${existingCount} already in Library` : "", skipped > 0n ? t`${skippedCount} without signatures` : ""].filter(Boolean);
  const [first = "", second = ""] = recordParts;
  const skippedRecords = recordParts.length === 2 ? t`${first}; ${second}` : first;
  if (files > 0n) {
    const fileCount = String(files);
    toast.success(skippedRecords ? t`Added ${fileCount} Files to Library; skipped ${skippedRecords}` : t`Added ${fileCount} Files to Library`);
    return;
  }
  toast.info(skippedRecords ? t`No new Files to add: ${skippedRecords}.` : t`No recorded files to add.`);
};

/**
 * Admits one Media's recorded content as independent Library Files. The count comes from a dry
 * run, and the confirmed call repeats the same request to write. Once the operator asks not to
 * see the confirmation again, which Library settings record, a later click imports directly. Explicit review always performs a dry run.
 */
export const useLibraryAdmission = (mediaID: bigint | undefined) => {
  const [state, setState] = useState<{ phase: "idle" | "calculating" | "confirm" | "importing" } | { phase: "error"; message: string }>({ phase: "idle" });
  const [files, setFiles] = useState(0n);
  const [existing, setExisting] = useState(0n);
  const [skipped, setSkipped] = useState(0n);
  const [directories, setDirectories] = useState(0n);
  const [skipPrompt, setSkipPrompt] = useState(false);
  const [promptSuppressed, setPromptSuppressed] = useState(false);
  const { skipped: confirmationSkipped, setSkipped: setConfirmationSkipped } = useSkippableConfirmation("skipAddMediaConfirmation");

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
        setState({ phase: "error", message: errorMessage(error, t`Could not inspect this Media`) });
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
      // The records are added; a choice that cannot be stored only keeps the confirmation as it was.
      if (remember && skipPrompt !== promptSuppressed) {
        await setConfirmationSkipped(skipPrompt).catch(() => toast.info(t`Could not save this choice.`));
      }
      admissionToast(reply.fileCount, reply.existingCount, reply.skippedFileCount);
      close();
    } catch (error) {
      setState({ phase: "error", message: errorMessage(error, t`Could not add these records`) });
    }
  };

  const review = () => {
    if (!mediaID || state.phase === "calculating" || state.phase === "importing") return;
    setPromptSuppressed(confirmationSkipped);
    setSkipPrompt(confirmationSkipped);
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
    restorePrompt: () =>
      void setConfirmationSkipped(false).then(
        () => {
          setPromptSuppressed(false);
          setSkipPrompt(false);
        },
        () => toast.info(t`Could not save this choice.`),
      ),
    review,
    ask: () => {
      if (!mediaID || state.phase === "calculating" || state.phase === "importing") return;
      if (confirmationSkipped) {
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
  const fileCount = String(admission.counts.files);
  return (
    <Dialog open onClose={() => (admission.busy ? undefined : admission.close())} maxWidth="sm" fullWidth>
      <DialogTitle>{t`Add existing inventory to Library`}</DialogTitle>
      <DialogContent>
        <Stack spacing={2}>
          {admission.calculating && (
            <Stack direction="row" spacing={1.5} sx={{ alignItems: "center" }}>
              <CircularProgress size={20} />
              <Typography>{t`Calculating what this Media would add…`}</Typography>
            </Stack>
          )}
          {!admission.calculating && !admission.error && !admission.busy && (
            <>
              <Typography>
                {admission.counts.files > 0n
                  ? name
                    ? t`${fileCount} files from ${name} will be added as independent Library files.`
                    : t`${fileCount} files will be added as independent Library files.`
                  : t`No new Files will be added.`}
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
                label={t`Don’t show this again`}
              />
            </>
          )}
          {admission.error && <Feedback severity="error">{admission.error}</Feedback>}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button type="button" variant="contained" disabled={admission.busy || !admission.canConfirm} onClick={admission.confirm}>
          {admission.busy ? t`Adding…` : t`Add to Library`}
        </Button>
        {admission.error && (
          <Button type="button" onClick={admission.retry}>
            {t`Retry`}
          </Button>
        )}
        {admission.promptSuppressed && (
          <Button type="button" disabled={admission.busy} onClick={admission.restorePrompt}>
            {t`Show confirmation next time`}
          </Button>
        )}
        <Button type="button" disabled={admission.busy} onClick={admission.close}>
          {t`Cancel`}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

const Summary = ({ counts }: { counts: { files: bigint; directories: bigint; existing: bigint; skipped: bigint } }) => {
  const directoryCount = String(counts.directories);
  const existingCount = String(counts.existing);
  const skippedCount = String(counts.skipped);
  const parts = [
    counts.directories > 0n ? t`${directoryCount} folder entries` : "",
    counts.existing > 0n ? t`${existingCount} already in Library` : "",
    counts.skipped > 0n ? t`${skippedCount} without signatures` : "",
  ].filter(Boolean);
  if (!parts.length && counts.files > 0n) return null;
  return (
    <Typography variant="body2" color="text.secondary">
      {parts.length ? parts.join(" · ") : t`No recorded files to add.`}
    </Typography>
  );
};
