import { Feedback } from "@/components/feedback";
import { type FormEvent, type ReactNode, useCallback, useId, useRef, useState } from "react";
import { Button, Checkbox, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, TextField } from "@mui/material";
import { toast } from "react-toastify";
import { errorMessage } from "@/tools";

type ActionRequest = {
  title: string;
  confirmLabel: string;
  cancelLabel?: string;
  children?: ReactNode;
  danger?: boolean;
  input?: { label: string; defaultValue?: string };
  /** Offers "Don't show this again" on an advisory confirmation and stores a ticked choice after it is confirmed. */
  onSkipNextTime?: () => Promise<void>;
  onConfirm: (value: string) => Promise<unknown> | void;
};

/** Application-owned confirmation with room for operation-specific content and options. */
export const ActionDialog = ({
  title,
  confirmLabel,
  cancelLabel = "Cancel",
  children,
  danger,
  input,
  onSkipNextTime,
  onConfirm,
  onClose,
}: ActionRequest & { onClose: () => void }) => {
  const [skipNextTime, setSkipNextTime] = useState(false);
  const titleID = useId();
  const [value, setValue] = useState(input?.defaultValue ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submitting = useRef(false);
  const cancelButton = useRef<HTMLButtonElement>(null);
  const valid = !input || value.length > 0;
  const close = () => {
    if (!submitting.current) onClose();
  };
  const submit = async () => {
    if (submitting.current || !valid) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      await onConfirm(value);
      // The confirmed action already happened; a choice that cannot be stored only keeps the confirmation.
      if (skipNextTime) await onSkipNextTime?.().catch(() => toast.info("Could not save this choice; the confirmation will appear again."));
      onClose();
    } catch (error) {
      setError(errorMessage(error, "Operation failed"));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={close}
      aria-labelledby={titleID}
      maxWidth="sm"
      fullWidth
      slotProps={{
        transition: {
          onEntered: () => {
            if (!input) cancelButton.current?.focus();
          },
        },
        paper: {
          component: "form",
          onSubmit: (event: FormEvent) => {
            event.preventDefault();
            event.stopPropagation();
            void submit();
          },
        },
      }}
    >
      <DialogTitle id={titleID}>{title}</DialogTitle>
      <DialogContent>
        {children}
        {input && (
          <TextField
            autoFocus
            required
            fullWidth
            multiline
            minRows={1}
            maxRows={4}
            margin="dense"
            label={input.label}
            value={value}
            disabled={busy}
            onChange={(event) => setValue(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
              event.preventDefault();
              void submit();
            }}
          />
        )}
        {onSkipNextTime && (
          <FormControlLabel
            control={<Checkbox checked={skipNextTime} disabled={busy} onChange={(_, checked) => setSkipNextTime(checked)} />}
            label="Don’t show this again"
          />
        )}
        {error && <Feedback severity="error">{error}</Feedback>}
      </DialogContent>
      <DialogActions>
        <Button type="submit" variant="contained" color={danger ? "error" : "primary"} disabled={busy || !valid}>
          {busy ? "Working…" : confirmLabel}
        </Button>
        <Button ref={cancelButton} type="button" autoFocus={!input} disabled={busy} onClick={close}>
          {cancelLabel}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export const useActionDialog = () => {
  const [request, setRequest] = useState<ActionRequest>();
  const ask = useCallback((next: ActionRequest) => setRequest((current) => current ?? next), []);
  return { ask, dialog: request && <ActionDialog {...request} onClose={() => setRequest(undefined)} /> };
};
