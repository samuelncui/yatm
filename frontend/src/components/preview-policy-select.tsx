import { MenuItem, TextField } from "@mui/material";
import { PreviewPolicy } from "@/entity";

export const PreviewPolicySelect = ({
  value,
  onChange,
  disabled = false,
  helperText,
}: {
  value: PreviewPolicy;
  onChange: (value: PreviewPolicy) => void;
  disabled?: boolean;
  helperText?: string;
}) => (
  <TextField
    select
    fullWidth
    label="Previews"
    value={value}
    disabled={disabled}
    helperText={helperText}
    onChange={(event) => onChange(Number(event.target.value))}
  >
    <MenuItem value={PreviewPolicy.PREVIEW_NONE}>Don’t generate</MenuItem>
    <MenuItem value={PreviewPolicy.PREVIEW_MISSING_ONLY}>Generate missing</MenuItem>
    <MenuItem value={PreviewPolicy.PREVIEW_REGENERATE_ALL}>Regenerate all</MenuItem>
  </TextField>
);
