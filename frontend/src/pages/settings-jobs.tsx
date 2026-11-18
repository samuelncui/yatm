import { t } from "@lingui/core/macro";
import { Grid, Stack } from "@mui/material";
import { useSettingsEditor } from "@/components/settings-editor";
import { SettingsNumberField } from "@/components/settings-field";
import { SettingsActions, SettingsEditorFeedback, SettingsSection } from "@/components/settings-page";
import { JobExecutionSettings, JobSettings, SettingsGroup } from "@/entity";

// Mirrors the service's rule that a result batch never exceeds the queue that feeds it.
const validateExecution = (execution: JobExecutionSettings): string =>
  execution.writeBatchSize > execution.writeBufferMax ? t`Results per update must not exceed queued results.` : "";

/** The Executor's pipeline limits: what an attempt batches, buffers and flushes while it runs. */
export const JobSettingsBrowser = () => {
  const editor = useSettingsEditor(SettingsGroup.JOB, JobSettings);
  const { cancel, dirty, draft, error, invalid, loading, reload, reject, save: saveDraft, saved, saving, setDraft } = editor;
  const execution = draft?.execution;
  const changeExecution = (changes: Partial<JobExecutionSettings>) =>
    setDraft((value) => (value?.execution ? { ...value, execution: { ...value.execution, ...changes } } : value));
  const validation = execution ? validateExecution(execution) : "";
  const save = async () => {
    if (!execution || saving || loading) return;
    if (validation) {
      reject(validation);
      return;
    }
    await saveDraft();
  };

  return (
    <>
      <SettingsEditorFeedback
        loading={loading}
        saving={saving}
        error={error}
        invalid={invalid}
        saved={saved}
        dirty={dirty}
        hasDraft={!!draft}
        onReload={reload}
      />
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
        {execution && (
          <SettingsSection title={t`Pipeline limits`}>
            <Grid container spacing={2}>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label={t`Manifest batch (items)`}
                  helperText={t`Items per manifest page. Does not change the full selection used for Tape planning.`}
                  value={execution.readBatch}
                  disabled={loading || saving}
                  onChange={(readBatch) => changeExecution({ readBatch })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label={t`Queued read items`}
                  helperText={t`Maximum queued reads. Larger queues can use more memory.`}
                  disabled={loading || saving}
                  value={execution.readBufferMax}
                  max={1000000}
                  onChange={(readBufferMax) => changeExecution({ readBufferMax })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label={t`Queued results`}
                  helperText={t`Maximum completed results waiting to be recorded; this is not a Tape byte buffer.`}
                  disabled={loading || saving}
                  value={execution.writeBufferMax}
                  max={1000000}
                  onChange={(writeBufferMax) => changeExecution({ writeBufferMax })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label={t`Results per update`}
                  helperText={validation || t`Maximum results in one update. Larger batches can delay progress reporting.`}
                  error={!!validation}
                  disabled={loading || saving}
                  value={execution.writeBatchSize}
                  onChange={(writeBatchSize) => changeExecution({ writeBatchSize })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label={t`Result flush interval (ms)`}
                  helperText={t`Maximum wait before a result update, in milliseconds.`}
                  disabled={loading || saving}
                  value={execution.flushIntervalMs}
                  min={100}
                  onChange={(flushIntervalMs) => changeExecution({ flushIntervalMs })}
                />
              </Grid>
            </Grid>
          </SettingsSection>
        )}
        <SettingsActions dirty={dirty} loading={loading} saving={saving} onCancel={cancel} />
      </Stack>
    </>
  );
};
