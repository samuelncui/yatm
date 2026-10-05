import { useMemo } from "react";
import moment from "moment";
import { AdapterMoment } from "@mui/x-date-pickers/AdapterMoment";
import { DateTimePicker } from "@mui/x-date-pickers/DateTimePicker";
import { LocalizationProvider } from "@mui/x-date-pickers/LocalizationProvider";
import { dateFromNs, minUnixNs, maxUnixNs } from "@/tools/time";

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
  const date = useMemo(() => (value ? moment(value, [storedFormat, moment.ISO_8601], true) : null), [value]);
  return (
    <LocalizationProvider dateAdapter={AdapterMoment}>
      <DateTimePicker
        label="Restore time"
        value={date}
        onChange={(next, context) => onChange(next === null ? "" : context.validationError ? "Invalid date" : next.format(storedFormat))}
        disabled={disabled}
        ampm={false}
        format="YYYY-MM-DD HH:mm"
        minDateTime={moment(dateFromNs(minUnixNs))}
        maxDateTime={moment(dateFromNs(maxUnixNs))}
        slotProps={{
          textField: { fullWidth: true, error, helperText: error ? "Choose a valid date and time." : undefined },
          actionBar: { actions: ["accept", "cancel"], sx: { justifyContent: "flex-start" } },
        }}
      />
    </LocalizationProvider>
  );
}
