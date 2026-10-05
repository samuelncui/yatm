import { Grid, Stack } from "@mui/material";
import { useSettingsEditor } from "@/components/settings-editor";
import { SettingsNumberField } from "@/components/settings-field";
import { SettingsActions, SettingsEditorFeedback, SettingsPage, SettingsSection } from "@/components/settings-page";
import { JobExecutionSettings, JobSettings, SettingsGroup } from "@/entity";

/** The Executor's pipeline limits: what an attempt batches, buffers and flushes while it runs. */
export const JobSettingsBrowser = () => {
  const editor = useSettingsEditor(SettingsGroup.JOB, JobSettings, "Job");
  const { cancel, dirty, draft, error, loading, reload, save: saveDraft, saved, saving, setDraft, setError } = editor;
  const execution = draft?.execution;
  const changeExecution = (changes: Partial<JobExecutionSettings>) =>
    setDraft((value) => (value?.execution ? { ...value, execution: { ...value.execution, ...changes } } : value));
  const save = async () => {
    if (!execution || saving || loading) return;
    const validation = validateExecution(execution);
    if (validation) {
      setError(validation);
      return;
    }
    await saveDraft();
  };

  return (
    <SettingsPage title="Jobs" description="These limits are captured when the next attempt starts. Running attempts keep their current limits.">
      <SettingsEditorFeedback label="Job" loading={loading} saving={saving} error={error} saved={saved} dirty={dirty} hasDraft={!!draft} onReload={reload} />
      {execution && (
        <Stack
          component="form"
          noValidate
          useFlexGap
          spacing={2}
          onSubmit={(event) => {
            event.preventDefault();
            void save();
          }}
        >
          <SettingsSection
            title="Job execution"
            description="Read batch is the manifest page; the buffers and the flush interval belong to the transfer engine."
          >
            <Grid container spacing={2}>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Read batch"
                  value={execution.readBatch}
                  disabled={loading || saving}
                  onChange={(readBatch) => changeExecution({ readBatch })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Read buffer"
                  disabled={loading || saving}
                  value={execution.readBufferMax}
                  max={1000000}
                  onChange={(readBufferMax) => changeExecution({ readBufferMax })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Write buffer"
                  disabled={loading || saving}
                  value={execution.writeBufferMax}
                  max={1000000}
                  onChange={(writeBufferMax) => changeExecution({ writeBufferMax })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Write batch"
                  disabled={loading || saving}
                  value={execution.writeBatchSize}
                  max={execution.writeBufferMax}
                  onChange={(writeBatchSize) => changeExecution({ writeBatchSize })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Flush interval (ms)"
                  disabled={loading || saving}
                  value={execution.flushIntervalMs}
                  min={100}
                  onChange={(flushIntervalMs) => changeExecution({ flushIntervalMs })}
                />
              </Grid>
            </Grid>
          </SettingsSection>
          <SettingsActions dirty={dirty} loading={loading} saving={saving} onCancel={cancel} />
        </Stack>
      )}
    </SettingsPage>
  );
};

// The service validates the same limits; this keeps a typo from becoming a round trip.
const validateExecution = (settings: JobExecutionSettings): string => {
  if (!Number.isInteger(settings.readBatch) || settings.readBatch < 1) return "Read batch must be at least 1.";
  if (!Number.isInteger(settings.readBufferMax) || settings.readBufferMax < 1 || settings.readBufferMax > 1000000)
    return "Read buffer must be between 1 and 1000000.";
  if (!Number.isInteger(settings.writeBufferMax) || settings.writeBufferMax < 1 || settings.writeBufferMax > 1000000)
    return "Write buffer must be between 1 and 1000000.";
  if (!Number.isInteger(settings.writeBatchSize) || settings.writeBatchSize < 1) return "Write batch must be at least 1.";
  if (settings.writeBatchSize > settings.writeBufferMax) return "Write batch must not exceed the write buffer.";
  if (!Number.isInteger(settings.flushIntervalMs) || settings.flushIntervalMs < 100) return "Flush interval must be at least 100 milliseconds.";
  return "";
};
