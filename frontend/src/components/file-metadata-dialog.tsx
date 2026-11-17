import { Feedback } from "@/components/feedback";
import { useEffect, useMemo, useState } from "react";
import { toast } from "react-toastify";

import Button from "@mui/material/Button";
import CircularProgress from "@mui/material/CircularProgress";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import FormControlLabel from "@mui/material/FormControlLabel";
import TextField from "@mui/material/TextField";
import { Checkbox, Stack, Typography } from "@mui/material";

import { filesCli } from "@/api";
import type { FileData } from "@samuelncui/chonky";
import { fileOperationReference } from "@/components/file-operations";
import { allowsFileOperation, filesEntryData } from "@/components/files-browser";
import { FileOperationKind } from "@/entity";
import { TagSelect } from "@/components/tag-select";
import { errorMessage } from "@/tools";
import { maxNoteLength, normalizeTags, tagDelta, unicodeLength, validateNote, validateTags } from "./file-metadata";

type Props = {
  files: FileData[];
  open: boolean;
  onClose: () => void;
  onSaved: () => Promise<void>;
};

// Single-file editing lives in the file details; this dialog edits a multi-file selection.
const title = "Edit tags & note";

const fileTags = (file: FileData) => {
  const tags = (file as FileData & { tags?: unknown }).tags;
  return Array.isArray(tags) && tags.every((tag): tag is string => typeof tag === "string") ? tags : [];
};

const sharedTags = (files: FileData[]) => {
  if (files.length === 0) return [];
  const first = normalizeTags(fileTags(files[0]));
  return first.filter((tag) => files.slice(1).every((file) => normalizeTags(fileTags(file)).includes(tag)));
};

const FileMetadataEditor = ({ files, onClose, onSaved }: Omit<Props, "open">) => {
  const initialTags = useMemo(() => sharedTags(files), [files]);
  const [tags, setTags] = useState<string[]>(initialTags);
  const [note, setNote] = useState("");
  const [replaceNote, setReplaceNote] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const effectiveTags = normalizeTags(tags);
  const { addTags, removeTags } = tagDelta(initialTags, effectiveTags);
  const tagError = validateTags(effectiveTags, addTags);
  const noteError = replaceNote ? validateNote(note) : "";
  const hasChanges = addTags.length > 0 || removeTags.length > 0 || replaceNote;
  const canSubmit = hasChanges && !tagError && !noteError && !submitting;

  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setError("");
    try {
      await filesCli.updateMetadata({
        references: files.map(fileOperationReference),
        addTags,
        removeTags,
        note: replaceNote ? note : undefined,
      }).response;
    } catch (error) {
      setError(errorMessage(error, "Could not save metadata"));
      setSubmitting(false);
      return;
    }

    setSubmitting(false);
    onClose();
    toast.success(`${files.length} files updated`);
    try {
      await onSaved();
    } catch (error) {
      toast.error(`Metadata saved, but refresh failed: ${errorMessage(error, "Refresh failed")}`);
    }
  };

  return (
    <Dialog open onClose={submitting ? undefined : onClose} maxWidth="sm" fullWidth>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent>
        <Stack spacing={2} sx={{ pt: 1 }}>
          <Typography variant="body2" color="text.secondary">
            {files.length} selected files
          </Typography>
          <TagSelect
            value={tags}
            onChange={setTags}
            disabled={submitting}
            error={Boolean(tagError)}
            helperText={tagError || "Shared tags are shown. Tags unique to individual files are kept."}
          />
          <FormControlLabel
            control={<Checkbox checked={replaceNote} onChange={(_, checked) => setReplaceNote(checked)} disabled={submitting} />}
            label="Replace the note on every selected file"
          />
          {replaceNote && (
            <TextField
              multiline
              minRows={3}
              maxRows={8}
              fullWidth
              label="Note"
              value={note}
              disabled={submitting}
              error={Boolean(noteError)}
              helperText={noteError || `${unicodeLength(note)}/${maxNoteLength} · Leave empty to clear every note.`}
              onChange={(event) => setNote(event.target.value)}
            />
          )}
          {error && <Feedback>{error}</Feedback>}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button
          variant="contained"
          onClick={() => void submit()}
          disabled={!canSubmit}
          startIcon={submitting ? <CircularProgress size={16} color="inherit" /> : undefined}
        >
          {submitting ? "Saving…" : "Save changes"}
        </Button>
        <Button onClick={onClose} disabled={submitting}>
          Cancel
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export const FileMetadataDialog = ({ files, open, onClose, onSaved }: Props) => {
  if (!open || files.length === 0) return null;
  const selectionKey = files.map((file) => file.id).join(",");
  return <LoadMetadata key={selectionKey} files={files} onClose={onClose} onSaved={onSaved} />;
};

const LoadMetadata = ({ files, onClose, onSaved }: Omit<Props, "open">) => {
  const [loaded, setLoaded] = useState<FileData[]>();
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    setError("");
    void (async () => {
      const values: FileData[] = [];
      for (const file of files) {
        const detail = (await filesCli.get({ reference: fileOperationReference(file) }).response).detail;
        if (!active) return;
        if (!detail?.entry) throw new Error("File details are unavailable. Refresh this folder.");
        const observed = filesEntryData(detail.entry);
        if (!allowsFileOperation(observed, FileOperationKind.UPDATE_METADATA)) throw new Error("Metadata editing is unavailable. Refresh this folder.");
        values.push({ ...file, ...observed, tags: detail.organization?.tags ?? [] });
      }
      if (active) setLoaded(values);
    })().catch((failure) => {
      if (active) setError(errorMessage(failure, "Could not load metadata"));
    });
    return () => {
      active = false;
    };
  }, [files, attempt]);
  if (loaded) return <FileMetadataEditor files={loaded} onClose={onClose} onSaved={onSaved} />;
  return (
    <Dialog open onClose={onClose}>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent>
        {error ? (
          <Feedback severity="error" action={<Button onClick={() => setAttempt((value) => value + 1)}>Retry</Button>}>
            {error}
          </Feedback>
        ) : (
          <CircularProgress size={24} />
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
      </DialogActions>
    </Dialog>
  );
};
