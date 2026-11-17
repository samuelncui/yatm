import { t } from "@lingui/core/macro";
import { Feedback } from "@/components/feedback";
import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "react-toastify";
import { Button, CircularProgress, TextField } from "@mui/material";

import { filesCli } from "@/api";
import type { FileOperationRef } from "@/entity";
import { errorMessage } from "@/tools";
import { TagSelect } from "./tag-select";
import { ActionRow } from "./action-row";
import { normalizeTags, tagDelta, validateNote, validateTags } from "./file-metadata";

type Metadata = { tags: string[]; note: string };
type Props = { reference: FileOperationRef; tags?: string[]; note: string; editable: boolean; onRefresh: () => Promise<void> };

const sameTags = (left: string[], right: string[]) => left.length === right.length && left.every((tag) => right.includes(tag));
const sameMetadata = (left: Metadata, right: Metadata) => left.note === right.note && sameTags(left.tags, right.tags);

export const InlineFileMetadata = ({ reference, tags, note, editable, onRefresh }: Props) => {
  const incoming = useMemo(() => ({ tags: normalizeTags(tags ?? []), note }), [tags, note]);
  const baselineRef = useRef(incoming);
  const [baseline, setBaseline] = useState(incoming);
  const [draft, setDraft] = useState(incoming);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    const previous = baselineRef.current;
    setDraft((current) => (sameMetadata(current, previous) ? incoming : current));
    baselineRef.current = incoming;
    setBaseline(incoming);
  }, [incoming]);

  const { addTags, removeTags } = tagDelta(baseline.tags, draft.tags);
  const noteChanged = draft.note !== baseline.note;
  const dirty = addTags.length > 0 || removeTags.length > 0 || noteChanged;
  const tagError = validateTags(draft.tags, addTags);
  const noteError = validateNote(draft.note);

  const save = async () => {
    if (!editable || !dirty || tagError || noteError || saving) return;
    setSaving(true);
    setError("");
    try {
      await filesCli.updateMetadata({
        references: [reference],
        addTags,
        removeTags,
        note: noteChanged ? draft.note : undefined,
      }).response;
    } catch (failure) {
      setError(errorMessage(failure, t`Could not save metadata`));
      setSaving(false);
      return;
    }
    baselineRef.current = draft;
    setBaseline(draft);
    setSaving(false);
    toast.success(t`File metadata updated`);
    try {
      await onRefresh();
    } catch (failure) {
      const reason = errorMessage(failure, t`Refresh failed`);
      toast.error(t`Metadata saved, but refresh failed: ${reason}`);
    }
  };

  return (
    <section className="file-inline-metadata" aria-label={t`File metadata`}>
      {editable ? (
        <>
          <TagSelect
            value={draft.tags}
            onChange={(next) => setDraft((current) => ({ ...current, tags: normalizeTags(next) }))}
            disabled={saving}
            error={Boolean(tagError)}
            helperText={tagError || undefined}
          />
          <TextField
            size="small"
            label={t`Note`}
            placeholder={t`Add context about this file`}
            value={draft.note}
            onChange={(event) => setDraft((current) => ({ ...current, note: event.target.value }))}
            disabled={saving}
            error={Boolean(noteError)}
            helperText={noteError || undefined}
            multiline
            minRows={2}
            maxRows={6}
            fullWidth
          />
          {error && <Feedback severity="error">{error}</Feedback>}
          <ActionRow className="file-inline-actions">
            <Button
              size="small"
              variant="contained"
              onClick={() => void save()}
              disabled={!dirty || Boolean(tagError) || Boolean(noteError) || saving}
              startIcon={saving ? <CircularProgress size={16} color="inherit" /> : undefined}
            >
              {saving ? t`Saving…` : t`Save changes`}
            </Button>
            <Button
              size="small"
              onClick={() => {
                setDraft(baseline);
                setError("");
              }}
              disabled={!dirty || saving}
            >
              {t`Cancel`}
            </Button>
          </ActionRow>
        </>
      ) : (
        <dl className="file-detail-summary">
          <dt>{t`Tags`}</dt>
          <dd>{draft.tags.length ? draft.tags.join(", ") : "—"}</dd>
          <dt>{t`Note`}</dt>
          <dd className="file-detail-note">{draft.note || "—"}</dd>
        </dl>
      )}
    </section>
  );
};
