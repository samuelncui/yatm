import { useEffect, useMemo, useState } from "react";
import { toast } from "react-toastify";

import CloseRoundedIcon from "@mui/icons-material/CloseRounded";
import DescriptionOutlinedIcon from "@mui/icons-material/DescriptionOutlined";
import LocalOfferOutlinedIcon from "@mui/icons-material/LocalOfferOutlined";
import NotesOutlinedIcon from "@mui/icons-material/NotesOutlined";
import Autocomplete from "@mui/material/Autocomplete";
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

import { cli, type LibraryFileData } from "@/api";

import "@/components/file-metadata-dialog.less";

type Props = {
  files: LibraryFileData[];
  open: boolean;
  onClose: () => void;
  onSaved: () => Promise<void>;
};

const maxTagLength = 128;
const maxAddedTags = 16;
const maxNoteLength = 4096;

const normalizeTags = (values: string[]) => Array.from(new Set(values.map((value) => value.trim().toLowerCase()).filter(Boolean)));

const unicodeLength = (value: string) => Array.from(value).length;

const sharedTags = (files: LibraryFileData[]) => {
  if (files.length === 0) return [];
  const first = normalizeTags(files[0].tags ?? []);
  return first.filter((tag) => files.slice(1).every((file) => normalizeTags(file.tags ?? []).includes(tag)));
};

const FileMetadataEditor = ({ files, onClose, onSaved }: Omit<Props, "open">) => {
  const single = files.length === 1 ? files[0] : undefined;
  const initialTags = useMemo(() => (single ? normalizeTags(single.tags ?? []) : sharedTags(files)), [files, single]);
  const [tags, setTags] = useState<string[]>(initialTags);
  const [note, setNote] = useState(single?.note ?? "");
  const [updateNote, setUpdateNote] = useState(Boolean(single));
  const [options, setOptions] = useState<string[]>([]);
  const [input, setInput] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    let active = true;
    const timer = window.setTimeout(() => {
      void cli
        .tagList({ prefix: input.trim(), limit: 50n })
        .response.then((reply) => {
          if (active) setOptions(reply.tags.map((tag) => tag.name));
        })
        .catch(() => {
          if (active) setOptions([]);
        });
    }, 200);
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [input]);

  const effectiveTags = normalizeTags(input ? [...tags, input] : tags);
  const addedTags = effectiveTags.filter((tag) => !initialTags.includes(tag));
  const removedTags = initialTags.filter((tag) => !effectiveTags.includes(tag));
  const tagError = effectiveTags.some((tag) => unicodeLength(tag) > maxTagLength)
    ? `Each tag can have at most ${maxTagLength} characters.`
    : addedTags.length > maxAddedTags
      ? `You can add at most ${maxAddedTags} tags at once.`
      : "";
  const noteLength = unicodeLength(note);
  const noteError = updateNote && noteLength > maxNoteLength ? `A note can have at most ${maxNoteLength} characters.` : "";
  const tagsChanged = addedTags.length > 0 || removedTags.length > 0;
  const noteChanged = updateNote && (single ? note !== single.note : true);
  const hasChanges = tagsChanged || noteChanged;
  const canSubmit = hasChanges && !tagError && !noteError && !submitting;

  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    try {
      const ids = files.map((file) => BigInt(file.id));
      await cli.fileMetadataEdit({ ids, addTags: addedTags, removeTags: removedTags, note: noteChanged ? note : undefined }).response;
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Update file metadata failed");
      setSubmitting(false);
      return;
    }

    setSubmitting(false);
    onClose();
    toast.success(files.length === 1 ? "File metadata updated" : `${files.length} files updated`);
    try {
      await onSaved();
    } catch (error) {
      toast.error(error instanceof Error ? `Metadata saved, but refresh failed: ${error.message}` : "Metadata saved, but refresh failed");
    }
  };

  const contextTitle = single?.name ?? `${files.length} selected files`;

  return (
    <Dialog className="file-metadata-dialog" open onClose={submitting ? undefined : onClose} maxWidth="md" fullWidth>
      <DialogTitle className="file-metadata-dialog-title">
        <span>
          <strong>Edit metadata</strong>
        </span>
        <IconButton aria-label="Close" onClick={onClose} disabled={submitting} size="small">
          <CloseRoundedIcon />
        </IconButton>
      </DialogTitle>
      <DialogContent className="file-metadata-dialog-content">
        <div className="file-metadata-context">
          <span className="file-metadata-context-icon">
            <DescriptionOutlinedIcon />
          </span>
          <span>
            <strong>{contextTitle}</strong>
            {!single && <span>Choose which metadata to update across the selection.</span>}
          </span>
        </div>

        <section className="file-metadata-section is-active">
          <header className="file-metadata-section-header">
            <span className="file-metadata-section-icon">
              <LocalOfferOutlinedIcon />
            </span>
            <span className="file-metadata-section-heading">
              <strong>Tags</strong>
            </span>
          </header>
          <div className="file-metadata-section-body">
            <Autocomplete
              multiple
              freeSolo
              disableClearable
              filterSelectedOptions
              options={options}
              value={tags}
              inputValue={input}
              disabled={submitting}
              onChange={(_, value) => setTags(normalizeTags(value))}
              onInputChange={(_, value) => setInput(value)}
              renderInput={(params) => (
                <TextField
                  {...params}
                  label="Tags"
                  placeholder={tags.length === 0 ? "Type a tag and press Enter" : "Add another tag"}
                  error={Boolean(tagError)}
                />
              )}
            />
            <div className="file-metadata-field-footer">
              <span className={tagError ? "is-error" : ""}>
                {tagError ||
                  (single ? "Tags are saved in lowercase and duplicate tags are removed." : "Shared tags are shown. Tags unique to individual files are kept.")}
              </span>
              <span>{single ? `${effectiveTags.length} tags` : `${effectiveTags.length} shared`}</span>
            </div>
          </div>
        </section>

        <section className={`file-metadata-section ${updateNote ? "is-active" : "is-disabled"}`}>
          <header className="file-metadata-section-header">
            <span className="file-metadata-section-icon">
              <NotesOutlinedIcon />
            </span>
            <span className="file-metadata-section-heading">
              <strong>Note</strong>
            </span>
            {!single && (
              <FormControlLabel
                className="file-metadata-section-toggle"
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
              onChange={(event) => setNote(event.target.value)}
            />
            <div className="file-metadata-field-footer">
              <span className={noteError ? "is-error" : ""}>
                {noteError || (single ? "Leave empty to clear the note." : "This replaces the note on every selected file.")}
              </span>
              <span>
                {noteLength}/{maxNoteLength}
              </span>
            </div>
          </div>
        </section>
      </DialogContent>
      <DialogActions className="file-metadata-dialog-actions">
        <span>{hasChanges ? "Ready to save" : "No changes to save"}</span>
        <div>
          <Button onClick={onClose} disabled={submitting}>
            Cancel
          </Button>
          <Button
            variant="contained"
            onClick={() => void submit()}
            disabled={!canSubmit}
            startIcon={submitting ? <CircularProgress size={16} color="inherit" /> : undefined}
          >
            {submitting ? "Saving…" : "Save changes"}
          </Button>
        </div>
      </DialogActions>
    </Dialog>
  );
};

export const FileMetadataDialog = ({ files, open, onClose, onSaved }: Props) => {
  if (!open || files.length === 0) return null;
  const selectionKey = files.map((file) => file.id).join(",");
  return <FileMetadataEditor key={selectionKey} files={files} onClose={onClose} onSaved={onSaved} />;
};
