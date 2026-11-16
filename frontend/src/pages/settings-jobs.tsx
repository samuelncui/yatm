import { Grid, Stack } from "@mui/material";
import { useSettingsEditor } from "@/components/settings-editor";
import { SettingsNumberField } from "@/components/settings-field";
import { SettingsActions, SettingsEditorFeedback, SettingsSection } from "@/components/settings-page";
import { JobExecutionSettings, JobSettings, SettingsGroup } from "@/entity";

/** The Executor's pipeline limits: what an attempt batches, buffers and flushes while it runs. */
export const JobSettingsBrowser = () => {
  const editor = useSettingsEditor(SettingsGroup.JOB, JobSettings, "Job");
  const { cancel, dirty, draft, error, invalid, loading, reload, save: saveDraft, saved, saving, setDraft } = editor;
  const execution = draft?.execution;
  const changeExecution = (changes: Partial<JobExecutionSettings>) =>
    setDraft((value) => (value?.execution ? { ...value, execution: { ...value.execution, ...changes } } : value));
  const save = async () => {
    if (!execution || saving || loading) return;
    await saveDraft();
  };

  return (
    <>
      <SettingsEditorFeedback
        label="Job"
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
          <SettingsSection
            title="Advanced"
            collapsible
            hasError={!!invalid}
            description={`Manifest batch ${execution.readBatch} items · queued reads ${execution.readBufferMax} · queued results ${execution.writeBufferMax} · ${execution.writeBatchSize} results/update · flush ${execution.flushIntervalMs} ms`}
          >
            <Grid container spacing={2}>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Manifest batch (items)"
                  helperText="Items per manifest page. Does not change the full selection used for Tape planning."
                  value={execution.readBatch}
                  disabled={loading || saving}
                  onChange={(readBatch) => changeExecution({ readBatch })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Queued read items"
                  helperText="Maximum queued reads. Larger queues can use more memory."
                  disabled={loading || saving}
                  value={execution.readBufferMax}
                  max={1000000}
                  onChange={(readBufferMax) => changeExecution({ readBufferMax })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Queued results"
                  helperText="Maximum completed results waiting to be recorded; this is not a Tape byte buffer."
                  disabled={loading || saving}
                  value={execution.writeBufferMax}
                  max={1000000}
                  onChange={(writeBufferMax) => changeExecution({ writeBufferMax })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Results per update"
                  helperText="Maximum results in one update. Larger batches can delay progress reporting."
                  disabled={loading || saving}
                  value={execution.writeBatchSize}
                  onChange={(writeBatchSize) => changeExecution({ writeBatchSize })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Result flush interval (ms)"
                  helperText="Maximum wait before a result update, in milliseconds."
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
