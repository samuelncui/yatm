import { MenuItem, TextField } from "@mui/material";
import { TarWriteOptions, TapeRecordMode } from "@/entity";

/** Preserve absent fields and explicit false when editing a tar FORMAT preference. */
export const TarWriteOptionsFields = ({
  value,
  disabled,
  effective,
  onChange,
}: {
  value: TarWriteOptions;
  disabled?: boolean;
  effective?: TarWriteOptions;
  onChange: (value: TarWriteOptions) => void;
}) => (
  <>
    <TextField
      select
      slotProps={{ select: { displayEmpty: true }, inputLabel: { shrink: true } }}
      fullWidth
      label="Record mode"
      value={value.recordMode ?? ""}
      error={!!recordModeError(value.recordMode)}
      helperText={
        recordModeError(value.recordMode) ||
        (value.recordMode === undefined
          ? effective?.recordMode === TapeRecordMode.FIXED
            ? "Inherited: Fixed."
            : effective?.recordMode === TapeRecordMode.VARIABLE
              ? "Inherited: Variable."
              : "Defaults to Variable unless set for the device or FORMAT."
          : effective
            ? "Choose Fixed or Variable, or inherit the device default."
            : "Leave unset to use Variable.")
      }
      disabled={disabled}
      onChange={(event) => onChange({ ...value, recordMode: event.target.value === "" ? undefined : Number(event.target.value) })}
    >
      <MenuItem value="">{effective ? "Use device default" : "Default (Variable)"}</MenuItem>
      <MenuItem value={TapeRecordMode.FIXED}>Fixed</MenuItem>
      <MenuItem value={TapeRecordMode.VARIABLE}>Variable</MenuItem>
    </TextField>
    <TextField
      fullWidth
      type="number"
      label="Record size (bytes)"
      placeholder="524288 (512 KiB)"
      value={value.recordBytes ?? ""}
      disabled={disabled}
      slotProps={{ htmlInput: { min: 512, max: 0xffffffff, step: 512 }, inputLabel: { shrink: true } }}
      error={!!recordSizeError(value.recordBytes)}
      helperText={
        recordSizeError(value.recordBytes) ||
        (value.recordBytes === undefined && effective?.recordBytes !== undefined
          ? `Inherited: ${effective.recordBytes} bytes. Sizes must be aligned to 512 bytes.`
          : "Defaults to 512 KiB (524288 bytes) unless set for the device or FORMAT. Sizes must be aligned to 512 bytes.")
      }
      onChange={(event) => onChange({ ...value, recordBytes: event.target.value === "" ? undefined : Number(event.target.value) })}
    />
    <TextField
      select
      slotProps={{ select: { displayEmpty: true }, inputLabel: { shrink: true } }}
      fullWidth
      label="Tar hardware compression"
      helperText={
        <>
          {value.hardwareCompression === undefined && effective
            ? effective.hardwareCompression === undefined
              ? "No device default or reported recommendation. Choose On or Off."
              : `Inherited: ${effective.hardwareCompression ? "On" : "Off"}.`
            : "Archive checks the selected drive's compression support before writing."}
          {" Hardware compression keeps reads in LOGICAL order."}
        </>
      }
      value={value.hardwareCompression === undefined ? "" : String(value.hardwareCompression)}
      disabled={disabled}
      onChange={(event) => onChange({ ...value, hardwareCompression: event.target.value === "" ? undefined : event.target.value === "true" })}
    >
      <MenuItem value="">{effective ? "Use device default or recommendation" : "Default (use recommendation)"}</MenuItem>
      <MenuItem value="false">Off</MenuItem>
      <MenuItem value="true">On</MenuItem>
    </TextField>
  </>
);

const recordModeError = (mode?: TapeRecordMode): string =>
  mode !== undefined && mode !== TapeRecordMode.FIXED && mode !== TapeRecordMode.VARIABLE ? "Choose Fixed or Variable record mode." : "";

const recordSizeError = (size?: number): string =>
  size !== undefined && (!Number.isInteger(size) || size < 512 || size > 0xffffffff || size % 512 !== 0)
    ? "Record size must be a multiple of 512 bytes between 512 and 4294966784."
    : "";

export const validateTarWriteOptions = (value: TarWriteOptions): string => recordModeError(value.recordMode) || recordSizeError(value.recordBytes);
