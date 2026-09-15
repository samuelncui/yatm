import { useMemo } from "react";
import moment from "moment";
import { AdapterMoment } from "@mui/x-date-pickers/AdapterMoment";
import { DateTimePicker } from "@mui/x-date-pickers/DateTimePicker";
import { LocalizationProvider } from "@mui/x-date-pickers/LocalizationProvider";

const storedFormat = "YYYY-MM-DDTHH:mm";

export default function RestoreTimePicker({
  value,
  onChange,
  disabled,
  error,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
  error: boolean;
}) {
  const date = useMemo(() => (value ? moment(value, storedFormat, true) : null), [value]);
  return (
    <LocalizationProvider dateAdapter={AdapterMoment}>
      <DateTimePicker
        label="Restore time"
        value={date}
        onChange={(next, context) => onChange(next === null ? "" : context.validationError ? "Invalid date" : next.format(storedFormat))}
        disabled={disabled}
        ampm={false}
        format="YYYY-MM-DD HH:mm"
        minDateTime={moment(0)}
        slotProps={{
          textField: { fullWidth: true, error, helperText: error ? "Choose a valid date and time." : undefined },
          actionBar: { actions: ["cancel", "accept"] },
        }}
      />
    </LocalizationProvider>
  );
}
