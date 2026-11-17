import { t } from "@lingui/core/macro";
import { useMemo } from "react";
import moment from "moment";
import { AdapterMoment } from "@mui/x-date-pickers/AdapterMoment";
import { DateTimePicker } from "@mui/x-date-pickers/DateTimePicker";
import { LocalizationProvider } from "@mui/x-date-pickers/LocalizationProvider";
import { zhCN } from "@mui/x-date-pickers/locales";
import { currentLocale } from "@/i18n";
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
  const zh = currentLocale() === "zh-CN";
  return (
    <LocalizationProvider
      dateAdapter={AdapterMoment}
      adapterLocale={zh ? "zh-cn" : "en"}
      localeText={zh ? zhCN.components.MuiLocalizationProvider.defaultProps.localeText : undefined}
    >
      <DateTimePicker
        label={t`Restore time`}
        value={date}
        onChange={(next, context) => onChange(next === null ? "" : context.validationError ? "Invalid date" : next.format(storedFormat))}
        disabled={disabled}
        ampm={false}
        format="YYYY-MM-DD HH:mm"
        minDateTime={moment(dateFromNs(minUnixNs))}
        maxDateTime={moment(dateFromNs(maxUnixNs))}
        slotProps={{
          textField: { fullWidth: true, error, helperText: error ? t`Choose a valid date and time.` : undefined },
          actionBar: { actions: ["accept", "cancel"], sx: { justifyContent: "flex-start" } },
        }}
      />
    </LocalizationProvider>
  );
}
