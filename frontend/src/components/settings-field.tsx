import { ReactElement, ReactNode } from "react";
import FormControl from "@mui/material/FormControl";
import FormControlLabel, { formControlLabelClasses } from "@mui/material/FormControlLabel";
import FormHelperText from "@mui/material/FormHelperText";
import TextField from "@mui/material/TextField";

/**
 * One settings control with the explanation that belongs to it: the label, the control and the
 * description travel together, so an explanation can never read as belonging to the next setting
 * or drift in spacing between screens.
 */
export const SettingsField = ({ label, control, description }: { label: string; control: ReactElement; description?: ReactNode }) => (
  <FormControl fullWidth>
    <FormControlLabel
      label={label}
      labelPlacement="start"
      control={control}
      sx={{ m: 0, justifyContent: "space-between", gap: 2, [`& .${formControlLabelClasses.label}`]: { fontWeight: 500 } }}
    />
    {description && <FormHelperText sx={{ m: 0, mt: 0.25 }}>{description}</FormHelperText>}
  </FormControl>
);

/** A whole-number setting, bounded by the same limits the service validates. */
export const SettingsNumberField = ({
  label,
  value,
  min = 1,
  max,
  helperText,
  disabled,
  onChange,
}: {
  label: string;
  value: number;
  min?: number;
  max?: number;
  helperText?: ReactNode;
  disabled?: boolean;
  onChange: (value: number) => void;
}) => (
  <TextField
    required
    fullWidth
    size="small"
    type="number"
    label={label}
    value={value || ""}
    helperText={helperText}
    disabled={disabled}
    slotProps={{ htmlInput: { min, max, step: 1 } }}
    onChange={(event) => onChange(Number(event.target.value))}
  />
);
