import { Feedback } from "@/components/feedback";
import { type FormEvent, type ReactNode, useCallback, useId, useRef, useState } from "react";
import { Button, Dialog, DialogActions, DialogContent, DialogTitle, TextField } from "@mui/material";
import { errorMessage } from "@/tools";

type ActionRequest = {
  title: string;
  confirmLabel: string;
  cancelLabel?: string;
  children?: ReactNode;
  danger?: boolean;
  input?: { label: string; defaultValue?: string };
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
  onConfirm,
  onClose,
}: ActionRequest & { onClose: () => void }) => {
  const titleID = useId();
  const [value, setValue] = useState(input?.defaultValue ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submitting = useRef(false);
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
        paper: {
          component: "form",
          onSubmit: (event: FormEvent) => {
            event.preventDefault();
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
        {error && <Feedback severity="error">{error}</Feedback>}
      </DialogContent>
      <DialogActions>
        <Button type="submit" variant="contained" color={danger ? "error" : "primary"} disabled={busy || !valid}>
          {busy ? "Working…" : confirmLabel}
        </Button>
        <Button type="button" autoFocus={!input} disabled={busy} onClick={close}>
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
