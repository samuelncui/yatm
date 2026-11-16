import { MenuItem, TextField } from "@mui/material";
import { TarWriteOptions, TapeRecordMode } from "@/entity";

/** Preserve absent fields when editing a tar FORMAT preference. */
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
      // Without an inherited default, unset means Variable, so it shows as Variable rather than as a separate entry.
      value={value.recordMode ?? (effective ? "" : TapeRecordMode.VARIABLE)}
      helperText={
        !effective
          ? "New tar FORMAT uses this unless the FORMAT overrides it."
          : value.recordMode === undefined
            ? effective.recordMode === TapeRecordMode.FIXED
              ? "Inherited: Fixed."
              : effective.recordMode === TapeRecordMode.VARIABLE
                ? "Inherited: Variable."
                : "Defaults to Variable unless set for the device or FORMAT."
            : "Choose Fixed or Variable, or inherit the device default."
      }
      disabled={disabled}
      onChange={(event) => onChange({ ...value, recordMode: event.target.value === "" ? undefined : Number(event.target.value) })}
    >
      {effective && <MenuItem value="">Use device default</MenuItem>}
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
  </>
);

const recordSizeError = (size?: number): string =>
  size !== undefined && (!Number.isInteger(size) || size < 512 || size > 0xffffffff || size % 512 !== 0)
    ? "Record size must be a multiple of 512 bytes between 512 and 4294966784."
    : "";

export const validateTarWriteOptions = (value: TarWriteOptions): string => recordSizeError(value.recordBytes);
