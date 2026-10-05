import { Feedback } from "@/components/feedback";
import { useEffect, useMemo, useState } from "react";
import { toast } from "react-toastify";

import CloseRoundedIcon from "@mui/icons-material/CloseRounded";
import DescriptionOutlinedIcon from "@mui/icons-material/DescriptionOutlined";
import Button from "@mui/material/Button";
import CircularProgress from "@mui/material/CircularProgress";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import FormControlLabel from "@mui/material/FormControlLabel";
import IconButton from "@mui/material/IconButton";
import Switch from "@mui/material/Switch";
import TextField from "@mui/material/TextField";
import { formControlLabelClasses, Typography } from "@mui/material";

import { filesCli } from "@/api";
import type { FileData } from "@samuelncui/chonky";
import { fileOperationReference } from "@/components/file-operations";
import { allowsFileOperation, filesEntryData } from "@/components/files-browser";
import { FileOperationKind } from "@/entity";
import { TagSelect } from "@/components/tag-select";
import { errorMessage } from "@/tools";
import { ActionRow } from "./action-row";
import { maxNoteLength, normalizeTags, tagDelta, unicodeLength, validateNote, validateTags } from "./file-metadata";

import "@/components/file-metadata-dialog.less";

type Props = {
  files: FileData[];
  open: boolean;
  onClose: () => void;
  onSaved: () => Promise<void>;
};

const fileTags = (file: FileData) => {
  const tags = (file as FileData & { tags?: unknown }).tags;
  return Array.isArray(tags) && tags.every((tag): tag is string => typeof tag === "string") ? tags : [];
};
const fileNote = (file: FileData) => (typeof (file as FileData & { note?: unknown }).note === "string" ? (file as FileData & { note: string }).note : "");

const sharedTags = (files: FileData[]) => {
  if (files.length === 0) return [];
  const first = normalizeTags(fileTags(files[0]));
  return first.filter((tag) => files.slice(1).every((file) => normalizeTags(fileTags(file)).includes(tag)));
};

const FileMetadataEditor = ({ files, onClose, onSaved }: Omit<Props, "open">) => {
  const single = files.length === 1 ? files[0] : undefined;
  const initialTags = useMemo(() => (single ? normalizeTags(fileTags(single)) : sharedTags(files)), [files, single]);
  const [tags, setTags] = useState<string[]>(initialTags);
  const [note, setNote] = useState(single ? fileNote(single) : "");
  const [updateNote, setUpdateNote] = useState(!!single);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const effectiveTags = normalizeTags(tags);
  const { addTags, removeTags } = tagDelta(initialTags, effectiveTags);
  const tagError = validateTags(effectiveTags, addTags);
  const noteLength = unicodeLength(note);
  const noteError = updateNote ? validateNote(note) : "";
  const tagsChanged = addTags.length > 0 || removeTags.length > 0;
  const noteChanged = updateNote && (single ? note !== fileNote(single) : true);
  const hasChanges = tagsChanged || noteChanged;
  const canSubmit = hasChanges && !tagError && !noteError && !submitting;
  const tagHint =
    tagError || (single ? "Tags are saved in lowercase and duplicate tags are removed." : "Shared tags are shown. Tags unique to individual files are kept.");

  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setError("");
    try {
      await filesCli.updateMetadata({
        references: files.map(fileOperationReference),
        addTags,
        removeTags,
        note: noteChanged ? note : undefined,
      }).response;
    } catch (error) {
      setError(errorMessage(error, "Could not save metadata"));
      setSubmitting(false);
      return;
    }

    setSubmitting(false);
    onClose();
    toast.success(files.length === 1 ? "File metadata updated" : `${files.length} files updated`);
    try {
      await onSaved();
    } catch (error) {
      toast.error(`Metadata saved, but refresh failed: ${errorMessage(error, "Refresh failed")}`);
    }
  };

  const contextTitle = single?.name ?? `${files.length} selected files`;

  return (
    <Dialog
      open
      onClose={submitting ? undefined : onClose}
      maxWidth="md"
      fullWidth
      slotProps={{ paper: { sx: { width: "min(680px, calc(100% - 32px))", maxWidth: 680, maxHeight: "calc(100% - 40px)", overflow: "hidden" } } }}
    >
      <DialogTitle className="file-metadata-dialog-title" sx={{ p: "18px 22px", borderBottom: 1, borderColor: "divider" }}>
        <span>
          <strong>Edit metadata</strong>
        </span>
        <IconButton aria-label="Close" onClick={onClose} disabled={submitting} size="small">
          <CloseRoundedIcon />
        </IconButton>
      </DialogTitle>
      <DialogContent className="file-metadata-dialog-content" sx={{ p: { xs: "14px", sm: "18px 22px" }, display: "grid", gap: "18px" }}>
        <div className="file-metadata-context">
          <span className="file-metadata-context-icon">
            <DescriptionOutlinedIcon />
          </span>
          <span>
            <strong>{contextTitle}</strong>
            {!single && <span>Choose which metadata to update across the selection.</span>}
          </span>
        </div>

        <section className="file-metadata-section">
          <header className="file-metadata-section-header">
            <span className="file-metadata-section-heading">
              <strong>Tags</strong>
            </span>
          </header>
          <div className="file-metadata-section-body">
            <TagSelect value={tags} onChange={setTags} disabled={submitting} error={Boolean(tagError)} helperText={tagError || undefined} />
            <div className="file-metadata-field-footer">
              <span>{!tagError && tagHint}</span>
              <span>{single ? `${effectiveTags.length} tags` : `${effectiveTags.length} shared`}</span>
            </div>
          </div>
        </section>

        <section className={`file-metadata-section ${updateNote ? "" : "is-disabled"}`}>
          <header className="file-metadata-section-header">
            <span className="file-metadata-section-heading">
              <strong>Note</strong>
            </span>
            {!single && (
              <FormControlLabel
                sx={{ m: 0, [`& .${formControlLabelClasses.label}`]: { color: "text.secondary", fontSize: 13 } }}
                control={<Switch checked={updateNote} onChange={(event) => setUpdateNote(event.target.checked)} disabled={submitting} />}
                label="Edit"
              />
            )}
          </header>
          <div className="file-metadata-section-body">
            <TextField
              multiline
              minRows={4}
              maxRows={8}
              fullWidth
              label="Note"
              placeholder={single ? "Add context about this file" : "Replace the note on every selected file"}
              value={note}
              disabled={!updateNote || submitting}
              error={Boolean(noteError)}
              helperText={noteError || undefined}
              onChange={(event) => setNote(event.target.value)}
            />
            <div className="file-metadata-field-footer">
              <span>{!noteError && (single ? "Leave empty to clear the note." : "This replaces the note on every selected file.")}</span>
              <span>
                {noteLength}/{maxNoteLength}
              </span>
            </div>
          </div>
        </section>
        {error && <Feedback>{error}</Feedback>}
      </DialogContent>
      <DialogActions disableSpacing sx={{ display: "block", minHeight: 64, p: "12px 22px", boxSizing: "border-box", borderTop: 1, borderColor: "divider" }}>
        <ActionRow>
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
          <Typography component="span" sx={{ ml: "auto", color: "text.secondary", fontSize: 12, display: { xs: "none", sm: "inline" } }}>
            {hasChanges ? "Ready to save" : "No changes to save"}
          </Typography>
        </ActionRow>
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
        if (Array.isArray(file.tags) && typeof file.note === "string") {
          if (!allowsFileOperation(file, FileOperationKind.UPDATE_METADATA)) throw new Error("Metadata editing is unavailable. Refresh this folder.");
          values.push(file);
          continue;
        }
        const detail = (await filesCli.get({ reference: fileOperationReference(file) }).response).detail;
        if (!active) return;
        if (!detail?.entry) throw new Error("File details are unavailable. Refresh this folder.");
        const observed = filesEntryData(detail.entry);
        if (!allowsFileOperation(observed, FileOperationKind.UPDATE_METADATA)) throw new Error("Metadata editing is unavailable. Refresh this folder.");
        values.push({ ...file, ...observed, tags: detail.organization?.tags ?? [], note: detail.organization?.note ?? "" });
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
      <DialogTitle>Edit metadata</DialogTitle>
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
