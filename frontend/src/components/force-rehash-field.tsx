import Checkbox from "@mui/material/Checkbox";
import FormControlLabel from "@mui/material/FormControlLabel";

export const ForceRehashField = ({ checked, onChange }: { checked: boolean; onChange: (checked: boolean) => void }) => (
  <FormControlLabel
    control={<Checkbox size="small" checked={checked} onChange={(event) => onChange(event.target.checked)} />}
    label="Force rehash · read file content even when a valid cached signature exists"
  />
);
