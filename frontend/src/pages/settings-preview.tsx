import { useState } from "react";
import {
  Alert,
  Autocomplete,
  Button,
  Checkbox,
  Chip,
  createFilterOptions,
  Divider,
  FormControl,
  Grid,
  LinearProgress,
  MenuItem,
  Stack,
  Switch,
  TextField,
  Typography,
} from "@mui/material";
import { useSettingsEditor } from "@/components/settings-editor";
import { SettingsField, SettingsNumberField } from "@/components/settings-field";
import { SettingsActions, SettingsEditorFeedback, SettingsPage, SettingsSection } from "@/components/settings-page";
import { PreviewSettings, type PreviewGeneratorSettings, SettingsGroup } from "@/entity";
import { usePreviewGeneration } from "@/components/preview-policy-select";

export const PreviewSettingsBrowser = () => {
  const editor = useSettingsEditor(SettingsGroup.PREVIEW, PreviewSettings, "Preview");
  const { cancel, dirty, draft, error, loading, reload, save: saveDraft, saved, saving, setDraft, setError } = editor;
  const [selected, setSelected] = useState(0);
  const generation = usePreviewGeneration(!!draft, draft);
  const save = async () => {
    if (!draft || saving || loading) return;
    const validation = validatePreview(draft);
    if (validation) {
      setError(validation.message);
      setSelected(validation.index);
      return;
    }
    if (await saveDraft()) generation.reload();
  };
  const activeSelected = draft ? Math.min(selected, Math.max(0, draft.generators.length - 1)) : 0;
  return (
    <SettingsPage title="Preview" description="Runtime controls apply to the next file; routes and output choices apply to new Jobs.">
      <SettingsEditorFeedback
        label="Preview"
        loading={loading}
        saving={saving}
        error={error}
        saved={saved}
        dirty={dirty}
        hasDraft={!!draft}
        onReload={reload}
      />
      {draft && (
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
          <SettingsSection title="Generation" description="Enablement changes apply when the next file starts.">
            <SettingsField
              label="Enable generation"
              control={<Switch checked={draft.enabled} disabled={loading || saving} onChange={(_, enabled) => setDraft({ ...draft, enabled })} />}
            />
            {!generation.loading && (generation.error || !generation.capabilities?.available) && (
              <Feedback severity="warning">{generation.error || generation.capabilities?.reason || generation.reason}</Feedback>
            )}
          </SettingsSection>
          <SettingsSection title="Runtime" description="Command and resource limits apply when the next file starts.">
            <Grid container spacing={2}>
              <Grid size={{ xs: 12, lg: 8 }}>
                <TextField
                  fullWidth
                  size="small"
                  label="Executable"
                  helperText="Leave empty to use yatm-preview beside the server."
                  value={draft.command}
                  disabled={loading || saving}
                  onChange={(event) => setDraft({ ...draft, command: event.target.value })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Concurrency"
                  value={draft.concurrency}
                  max={16}
                  disabled={loading || saving}
                  onChange={(concurrency) => setDraft({ ...draft, concurrency })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Timeout (seconds)"
                  value={draft.timeoutSeconds}
                  max={86400}
                  disabled={loading || saving}
                  onChange={(timeoutSeconds) => setDraft({ ...draft, timeoutSeconds })}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 4 }}>
                <SettingsNumberField
                  label="Maximum input pixels"
                  value={Number(draft.maxInputPixels)}
                  max={64000000}
                  disabled={loading || saving}
                  onChange={(value) => setDraft({ ...draft, maxInputPixels: BigInt(Number.isFinite(value) ? Math.trunc(value) : 0) })}
                />
              </Grid>
            </Grid>
          </SettingsSection>
          {draft.generators.length === 0 && <Alert severity="info">No Preview generators are configured.</Alert>}
          {draft.generators.length > 0 && (
            <SettingsSection title="Output" description="File routes, dimensions, format and quality are captured by new Jobs.">
              <Stack useFlexGap spacing={2}>
                <Autocomplete
                  disableClearable
                  openOnFocus
                  size="small"
                  disabled={loading || saving}
                  options={draft.generators.map((_, index) => index)}
                  value={activeSelected}
                  getOptionLabel={(index) => generatorLabel(draft, index)}
                  filterOptions={createFilterOptions<number>({
                    stringify: (index) => generatorLabel(draft, index) + " " + draft.generators[index].extensions.map((entry) => entry.name).join(" "),
                  })}
                  onChange={(_, index) => setSelected(index)}
                  renderInput={(params) => <TextField {...params} label="Type" />}
                />
                {draft.generators[activeSelected] && (
                  <GeneratorForm
                    key={activeSelected}
                    generator={draft.generators[activeSelected]}
                    label={generatorLabel(draft, activeSelected)}
                    supportedExtensions={generation.capabilities?.inputExtensionsByKind?.[draft.generators[activeSelected].options.oneofKind ?? ""]?.extensions}
                    disabled={loading || saving}
                    onChange={(value) => {
                      setDraft({ ...draft, generators: draft.generators.map((existing, item) => (item === activeSelected ? value : existing)) });
                    }}
                  />
                )}
              </Stack>
            </SettingsSection>
          )}
          <SettingsSection title="Supported formats" description="Capabilities reported by the current Preview helper.">
            <Stack useFlexGap spacing={1}>
              {generation.capabilities && (
                <>
                  <div>
                    Helper {generation.capabilities.version || "unavailable"}
                    {generation.capabilities.ffmpegVersion && ` · FFmpeg ${generation.capabilities.ffmpegVersion}`}
                    {generation.capabilities.librawVersion && ` · LibRaw ${generation.capabilities.librawVersion}`}
                  </div>
                  {Object.entries(generation.capabilities.inputExtensionsByKind ?? {})
                    .sort(([a], [b]) => a.localeCompare(b))
                    .map(([kind, formats]) => (
                      <div key={kind}>
                        {kind === "image" ? "Image" : kind === "video" ? "Video" : kind}: {formats.extensions.join(", ")}
                      </div>
                    ))}
                  <div>Output: {generation.capabilities.outputFormats?.join(", ")}</div>
                </>
              )}
              {generation.loading && <LinearProgress aria-label="Checking Preview helper" />}
              <Button type="button" sx={{ alignSelf: "flex-start" }} disabled={loading || saving || generation.loading} onClick={generation.reload}>
                Check again
              </Button>
            </Stack>
          </SettingsSection>
          <SettingsActions dirty={dirty} loading={loading} saving={saving} onCancel={cancel} />
        </Stack>
      )}
    </SettingsPage>
  );
};

const GeneratorForm = ({
  generator,
  label,
  supportedExtensions,
  disabled,
  onChange,
}: {
  generator: PreviewGeneratorSettings;
  label: string;
  supportedExtensions?: string[];
  disabled: boolean;
  onChange: (value: PreviewGeneratorSettings) => void;
}) => {
  const { options } = generator;
  if (!options.oneofKind) return <Alert severity="warning">This preview type has no supported settings.</Alert>;
  const value = options.oneofKind === "image" ? options.image : options.video;
  const change = (field: string, next: string | number) => {
    const nextOptions =
      options.oneofKind === "image"
        ? { oneofKind: "image" as const, image: { ...options.image, [field]: next } }
        : { oneofKind: "video" as const, video: { ...options.video, [field]: next } };
    onChange({ ...generator, options: nextOptions });
  };
  const dimensions =
    options.oneofKind === "image"
      ? [
          { field: "maxWidth", label: "Maximum width", value: options.image.maxWidth },
          { field: "maxHeight", label: "Maximum height", value: options.image.maxHeight },
        ]
      : [
          { field: "posterWidth", label: "Poster width", value: options.video.posterWidth },
          { field: "posterHeight", label: "Poster height", value: options.video.posterHeight },
          { field: "timelineWidth", label: "Timeline tile width", value: options.video.timelineWidth },
          { field: "timelineHeight", label: "Timeline tile height", value: options.video.timelineHeight },
        ];
  return (
    <FormControl component="fieldset" fullWidth disabled={disabled} aria-label={label}>
      <Stack useFlexGap spacing={2}>
        <SettingsField
          label="Enabled"
          control={<Switch checked={generator.enabled} disabled={disabled} onChange={(_, enabled) => onChange({ ...generator, enabled })} />}
        />
        <Grid container spacing={2}>
          {dimensions.map(({ field, label, value }) => (
            <Grid key={field} size={{ xs: 12, sm: 6, lg: 3 }}>
              <SettingsNumberField label={label + " (px)"} value={value} max={8192} disabled={disabled} onChange={(next) => change(field, next)} />
            </Grid>
          ))}
          {options.oneofKind === "video" && (
            <>
              <Grid size={{ xs: 12, sm: 6, lg: 3 }}>
                <SettingsNumberField
                  label="Sampling interval (seconds)"
                  value={options.video.timelineIntervalSeconds}
                  max={2147483647}
                  disabled={disabled}
                  onChange={(next) => change("timelineIntervalSeconds", next)}
                />
              </Grid>
              <Grid size={{ xs: 12, sm: 6, lg: 3 }}>
                <SettingsNumberField
                  label="Maximum frames"
                  value={options.video.timelineMaxFrames}
                  max={1000}
                  disabled={disabled}
                  onChange={(next) => change("timelineMaxFrames", next)}
                />
              </Grid>
            </>
          )}
          <Grid size={{ xs: 12, sm: 6, lg: 3 }}>
            <TextField
              select
              fullWidth
              disabled={disabled}
              size="small"
              label="Output format"
              value={value.format}
              onChange={(event) => change("format", event.target.value)}
            >
              <MenuItem value="webp">WebP</MenuItem>
              <MenuItem value="jpg">JPEG</MenuItem>
              {value.format === "jpeg" && <MenuItem value="jpeg">JPEG</MenuItem>}
              <MenuItem value="png">PNG</MenuItem>
            </TextField>
          </Grid>
          <Grid size={{ xs: 12, sm: 6, lg: 3 }}>
            <SettingsNumberField label="Quality" value={value.quality} max={100} disabled={disabled} onChange={(next) => change("quality", next)} />
          </Grid>
        </Grid>
        <Divider />
        <Typography variant="subtitle2">File routing</Typography>
        <Autocomplete
          multiple
          freeSolo
          clearOnBlur
          disableCloseOnSelect
          size="small"
          disabled={disabled}
          options={[...new Set([...(supportedExtensions ?? []), ...generator.extensions.map((entry) => entry.name)])]}
          value={generator.extensions.filter((entry) => entry.enabled).map((entry) => entry.name)}
          onChange={(_, names, reason) => {
            const selected = new Set(names.map((name) => name.trim().toLowerCase().replace(/^\./, "")).filter(Boolean));
            const existing = new Set(generator.extensions.map((entry) => entry.name));
            onChange({
              ...generator,
              extensions: [
                ...generator.extensions
                  .filter((entry) => reason !== "clear" || !entry.enabled)
                  .map((entry) => ({ ...entry, enabled: selected.has(entry.name) })),
                ...[...selected].filter((name) => !existing.has(name)).map((name) => ({ name, enabled: true })),
              ],
            });
          }}
          renderOption={(props, option, { selected }) => (
            <li {...props} key={option}>
              <Checkbox checked={selected} tabIndex={-1} disableRipple />
              {option}
              {supportedExtensions?.includes(option) ? "" : " · unconfirmed"}
            </li>
          )}
          renderValue={(values, getItemProps) =>
            values.map((value, index) => {
              const { key, ...props } = getItemProps({ index });
              return (
                <Chip
                  key={key}
                  {...props}
                  aria-disabled={disabled || undefined}
                  onDelete={disabled ? undefined : () => onChange({ ...generator, extensions: generator.extensions.filter((entry) => entry.name !== value) })}
                  onKeyDown={(event) => {
                    // Let Chip own deletion instead of Autocomplete turning it into an uncheck.
                    if (event.key === "Backspace" || event.key === "Delete") event.stopPropagation();
                  }}
                  size="small"
                  label={value}
                  color={supportedExtensions?.includes(value) ? "default" : "warning"}
                />
              );
            })
          }
          renderInput={(params) => <TextField {...params} label="File extensions" />}
        />
      </Stack>
    </FormControl>
  );
};

const generatorLabel = (settings: PreviewSettings, index: number): string => {
  const generator = settings.generators[index];
  if (!generator) return "";
  const kind = generator.options.oneofKind;
  const name = kind === "image" ? "Image" : kind === "video" ? "Video" : "Unsupported type";
  if (settings.generators.filter((entry) => entry.options.oneofKind === kind).length < 2) return name;
  return `${name} · ${generator.extensions.map((entry) => entry.name).join(", ")}`;
};

const validatePreview = (settings: PreviewSettings): { index: number; message: string } | undefined => {
  if (!Number.isInteger(settings.concurrency) || settings.concurrency < 1 || settings.concurrency > 16)
    return { index: 0, message: "Concurrency must be between 1 and 16." };
  if (!Number.isInteger(settings.timeoutSeconds) || settings.timeoutSeconds < 1 || settings.timeoutSeconds > 86400)
    return { index: 0, message: "Timeout must be between 1 and 86400 seconds." };
  if (settings.maxInputPixels < 1n || settings.maxInputPixels > 64000000n) return { index: 0, message: "Maximum input pixels must be between 1 and 64000000." };
  const extensions = new Set<string>();
  for (const [index, generator] of settings.generators.entries()) {
    const invalid = (message: string) => ({ index, message: `${generatorLabel(settings, index)}: ${message}` });
    if (!generator.extensions.length) return invalid("Enter at least one file extension.");
    for (const entry of generator.extensions) {
      const extension = entry.name.trim().toLowerCase().replace(/^\./, "");
      if (!extension || /[/\\,\s]/.test(extension)) return invalid("Enter a valid file extension.");
      if (extensions.has(extension)) return invalid(`The extension "${extension}" is assigned more than once.`);
      extensions.add(extension);
    }
    const { options } = generator;
    if (!options.oneofKind) return invalid("Choose supported preview settings.");
    const values = options.oneofKind === "image" ? options.image : options.video;
    for (const [field, value] of Object.entries(values)) {
      if (typeof value === "string" && !value.trim()) return invalid("Complete all required fields.");
      if (typeof value !== "number") continue;
      const limit = field === "quality" ? 100 : field === "timelineMaxFrames" ? 1000 : field === "timelineIntervalSeconds" ? 2147483647 : 8192;
      if (!Number.isInteger(value) || value < 1 || value > limit) return invalid("Enter valid dimensions, quality and sampling values.");
    }
    if (options.oneofKind === "video") {
      const video = options.video;
      const columns = Math.min(video.timelineMaxFrames, 10);
      const rows = Math.ceil(video.timelineMaxFrames / columns);
      if (columns * rows * video.timelineWidth * video.timelineHeight > 100000000)
        return invalid("Reduce the timeline tile size or maximum frames to stay within 100 million pixels.");
    }
  }
  return undefined;
};
import { Feedback } from "@/components/feedback";
