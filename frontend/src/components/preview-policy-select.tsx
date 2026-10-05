import { useEffect, useState } from "react";
import { Link, MenuItem, TextField } from "@mui/material";
import { previewCli } from "@/api";
import { PreviewPolicy, SettingsGroup, type GetPreviewCapabilitiesResponse, type PreviewSettings } from "@/entity";
import { errorMessage } from "@/tools";
import { useCommittedSettings } from "@/components/settings-editor";

export function usePreviewGeneration(active = true, settings?: PreviewSettings) {
  const [capabilities, setCapabilities] = useState<GetPreviewCapabilitiesResponse>();
  const committed = useCommittedSettings(SettingsGroup.PREVIEW, active && !settings);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    setLoading(true);
    void previewCli
      .getCapabilities({}, { abort: controller.signal })
      .response.then((reply) => {
        if (controller.signal.aborted) return;
        setCapabilities(reply);
        setError("");
      })
      .catch((failure) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(failure, "Could not check Preview generation"));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [active, attempt]);
  const generationEnabled = settings?.enabled ?? committed.settings?.enabled;
  const pending = loading || (!settings && committed.loading);
  const failure = error || (!settings ? committed.error : "");
  let reason = failure;
  if (pending) reason = "Checking Preview generation…";
  else if (!failure && !generationEnabled) reason = "Preview generation is disabled.";
  else if (!failure && !capabilities?.available) reason = capabilities?.reason || "Preview helper unavailable.";
  return {
    capabilities,
    loading: pending,
    error: failure,
    reason,
    available: !pending && !failure && !!generationEnabled && !!capabilities?.available,
    reload: () => {
      setAttempt((value) => value + 1);
      if (!settings) void committed.reload().catch(() => {});
    },
  };
}

export const PreviewPolicySelect = ({
  value,
  onChange,
  disabled = false,
  helperText,
  generation,
}: {
  value: PreviewPolicy;
  onChange: (value: PreviewPolicy) => void;
  disabled?: boolean;
  helperText?: string;
  generation?: ReturnType<typeof usePreviewGeneration>;
}) => (
  <TextField
    select
    fullWidth
    label="Previews"
    value={value}
    disabled={disabled}
    helperText={
      helperText ||
      (generation?.reason && (
        <>
          {generation.reason} <Link href="/settings/preview">Settings</Link>
        </>
      ))
    }
    onChange={(event) => onChange(Number(event.target.value))}
  >
    <MenuItem value={PreviewPolicy.NONE}>Don’t generate</MenuItem>
    <MenuItem disabled={generation && !generation.available} value={PreviewPolicy.MISSING_ONLY}>
      Generate missing
    </MenuItem>
    <MenuItem disabled={generation && !generation.available} value={PreviewPolicy.REGENERATE_ALL}>
      Regenerate all
    </MenuItem>
  </TextField>
);
