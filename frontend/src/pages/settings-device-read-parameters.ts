import { msg, t } from "@lingui/core/macro";
import type { MessageDescriptor } from "@lingui/core";
import { TapeReadParameters, type InspectDeviceResponse } from "@/entity";
import { densityLabel } from "@/components/tape-density";

const maxDurationSeconds = 9223372036.854776;

// validateTapeReadParameters checks one set: it names its density code, and supplied values are usable.
export const validateTapeReadParameters = (value: TapeReadParameters): string => {
  const scope = value.appliesTo;
  if (scope?.densityCode === undefined) return t`Read parameters density code is required.`;
  if (!Number.isInteger(scope.densityCode) || scope.densityCode < 0 || scope.densityCode > 255)
    return t`Read parameters density code must be between 0 and 255.`;
  if (scope.driveSerial !== undefined && !scope.driveSerial.trim()) return t`Read parameters drive serial must be nonempty when supplied.`;
  const nominalWraps = value.geometry?.nominalWraps;
  if (nominalWraps !== undefined && (!Number.isInteger(nominalWraps) || nominalWraps <= 0 || nominalWraps > 0xffffffff))
    return t`Read parameters nominal wraps must be a positive uint32.`;
  const positioning = value.positioning;
  const reading = value.reading;
  const completion = value.completion;
  if (completion?.fullSpanSeconds !== undefined && completion.fullSpanSeconds <= 0) return t`Read parameters full-span unload seconds must be positive.`;
  // Each duration carries its two messages, so every error is one whole sentence.
  const seconds: [number | undefined, () => string, () => string][] = [
    [
      positioning?.baseSeconds,
      () => t`Read parameters base must be finite and representable as a duration.`,
      () => t`Read parameters base must be nonnegative.`,
    ],
    [
      positioning?.approachReversalSeconds,
      () => t`Read parameters approach reversal must be finite and representable as a duration.`,
      () => t`Read parameters approach reversal must be nonnegative.`,
    ],
    [
      positioning?.seekSecondsPerWrap,
      () => t`Read parameters seek seconds per wrap must be finite and representable as a duration.`,
      () => t`Read parameters seek seconds per wrap must be nonnegative.`,
    ],
    [
      reading?.filemarkOverheadSeconds,
      () => t`Read parameters filemark overhead must be finite and representable as a duration.`,
      () => t`Read parameters filemark overhead must be nonnegative.`,
    ],
    [
      completion?.fullSpanSeconds,
      () => t`Read parameters full-span unload must be finite and representable as a duration.`,
      () => t`Read parameters full-span unload must be nonnegative.`,
    ],
    [
      completion?.fixedSeconds,
      () => t`Read parameters fixed unload must be finite and representable as a duration.`,
      () => t`Read parameters fixed unload must be nonnegative.`,
    ],
    [
      completion?.ltfsUnmountSeconds,
      () => t`Read parameters LTFS unmount must be finite and representable as a duration.`,
      () => t`Read parameters LTFS unmount must be nonnegative.`,
    ],
  ];
  for (const [number, notFinite, negative] of seconds) {
    if (number === undefined) continue;
    if (!Number.isFinite(number) || Math.abs(number) >= maxDurationSeconds) return notFinite();
    if (number < 0) return negative();
  }
  if (positioning?.streamingBytes !== undefined && (positioning.streamingBytes < 0n || positioning.streamingBytes > 0xffffffffffffffffn))
    return t`Read parameters streaming bytes must be a uint64 value.`;
  if (reading?.bytesPerSecond !== undefined && (!Number.isFinite(reading.bytesPerSecond) || reading.bytesPerSecond <= 0))
    return t`Read parameters bytes per second must be finite and positive.`;
  return "";
};

// validateTapeReadParameterSets checks every set of one device and that density codes are unique.
export const validateTapeReadParameterSets = (values: TapeReadParameters[]): string => {
  const densities = new Set<number>();
  for (const value of values) {
    const error = validateTapeReadParameters(value);
    if (error) return error;
    const density = value.appliesTo!.densityCode!;
    if (densities.has(density)) return t`Read parameters for density code ${density} are repeated.`;
    densities.add(density);
  }
  return "";
};

export const captureTapeReadConditions = (report: InspectDeviceResponse): TapeReadParameters => {
  const tape = report.report.oneofKind === "tape" ? report.report.tape : undefined;
  return TapeReadParameters.create({
    appliesTo: {
      driveSerial: report.serialNumber?.trim() ? report.serialNumber : undefined,
      densityCode: tape?.densityCode,
    },
  });
};

/** Parses a density code typed as decimal or 0x-prefixed hexadecimal; NaN when it is not one. */
export const parseDensityCode = (text: string): number => {
  const value = text.trim();
  const code = /^0x[0-9a-f]{1,2}$/i.test(value) ? parseInt(value.slice(2), 16) : /^\d{1,3}$/.test(value) ? Number(value) : NaN;
  return code <= 255 ? code : NaN;
};

type Section = "positioning" | "reading" | "completion" | "geometry";
// seconds: nonnegative duration; positiveSeconds: also above zero; rate: MB/s stored as bytes per
// second; megabytes: decimal MB stored as whole bytes; wraps: positive uint32.
type Kind = "seconds" | "positiveSeconds" | "rate" | "megabytes" | "wraps";
type Stored = number | bigint | undefined;

export type ReadParameterField = {
  key: string;
  section: Section;
  field: string;
  kind: Kind;
  label: MessageDescriptor;
  helper: MessageDescriptor;
};

export const readParameterGroups: { title: MessageDescriptor; fields: ReadParameterField[] }[] = [
  {
    title: msg`Positioning`,
    fields: [
      {
        key: "baseSeconds",
        section: "positioning",
        field: "baseSeconds",
        kind: "seconds",
        label: msg`Fixed positioning time (s)`,
        helper: msg`Fixed time of every LOCATE the drive does not stream through.`,
      },
      {
        key: "seekSecondsPerWrap",
        section: "positioning",
        field: "seekSecondsPerWrap",
        kind: "seconds",
        label: msg`Seek time for one complete span (s)`,
        helper: msg`Time to move one complete longitudinal tape span at seek speed, not the time to read a wrap.`,
      },
      {
        key: "approachReversalSeconds",
        section: "positioning",
        field: "approachReversalSeconds",
        kind: "seconds",
        label: msg`Added time when arriving against the wrap direction (s)`,
        helper: msg`Also added once for each wrap turn crossed while reading.`,
      },
      {
        key: "streamingBytes",
        section: "positioning",
        field: "streamingBytes",
        kind: "megabytes",
        label: msg`Streaming distance (MB)`,
        helper: msg`How far ahead the drive reaches by continuing to read instead of positioning. Empty or 0 never applies.`,
      },
    ],
  },
  {
    title: msg`Reading`,
    fields: [
      {
        key: "bytesPerSecond",
        section: "reading",
        field: "bytesPerSecond",
        kind: "rate",
        label: msg`Read rate (MB/s)`,
        helper: msg`Reading speed in decimal megabytes per second; it also prices a read-speed locate.`,
      },
      {
        key: "filemarkOverheadSeconds",
        section: "reading",
        field: "filemarkOverheadSeconds",
        kind: "seconds",
        label: msg`Filemark overhead (s)`,
        helper: msg`Added for each crossed filemark and the following READ. Empty leaves it unpriced.`,
      },
    ],
  },
  {
    title: msg`Completion`,
    fields: [
      {
        key: "fixedSeconds",
        section: "completion",
        field: "fixedSeconds",
        kind: "seconds",
        label: msg`Fixed time (s)`,
        helper: msg`Fixed part of the normal rewind and unload.`,
      },
      {
        key: "fullSpanSeconds",
        section: "completion",
        field: "fullSpanSeconds",
        kind: "positiveSeconds",
        label: msg`Full-span time (s)`,
        helper: msg`Rewind and unload time across one physical tape length; must be above 0.`,
      },
      {
        key: "ltfsUnmountSeconds",
        section: "completion",
        field: "ltfsUnmountSeconds",
        kind: "seconds",
        label: msg`LTFS unmount time (s)`,
        helper: msg`Added for the LTFS unmount script and finalization; tar ignores it.`,
      },
    ],
  },
  {
    title: msg`Geometry`,
    fields: [
      {
        key: "nominalWraps",
        section: "geometry",
        field: "nominalWraps",
        kind: "wraps",
        label: msg`Nominal wraps`,
        helper: msg`Whole number of wraps on the cartridge; required to derive geometry from a live wrap report.`,
      },
    ],
  },
];

const readParameterFields = readParameterGroups.flatMap((group) => group.fields);

const storedValue = (value: TapeReadParameters, field: ReadParameterField): Stored =>
  (value[field.section] as Record<string, Stored> | undefined)?.[field.field];

// shiftDecimal moves the decimal point of a nonnegative decimal text six places right, exactly.
const shiftDecimal = (text: string): { whole: bigint; fraction: string } | undefined => {
  const match = /^(\d*)(?:\.(\d*))?$/.exec(text);
  if (!match || !(match[1] || match[2])) return undefined;
  const digits = (match[2] ?? "").padEnd(6, "0");
  return { whole: BigInt((match[1] || "0") + digits.slice(0, 6)), fraction: digits.slice(6).replace(/0+$/, "") };
};

const formatText = (field: ReadParameterField, value: Stored): string => {
  if (value === undefined) return "";
  if (field.kind === "megabytes") {
    const bytes = value as bigint;
    const fraction = (bytes % 1000000n).toString().padStart(6, "0").replace(/0+$/, "");
    return `${bytes / 1000000n}${fraction ? `.${fraction}` : ""}`;
  }
  if (field.kind === "rate") return String((value as number) / 1e6);
  return String(value);
};

// parseText converts one field's text to its stored value, or names why it is not usable.
const parseText = (field: ReadParameterField, input: string): { value?: Stored; error?: string } => {
  const text = input.trim();
  if (!text) return { value: undefined };
  if (field.kind === "megabytes" || field.kind === "rate") {
    const shifted = shiftDecimal(text);
    if (!shifted) return { error: t`Enter a nonnegative decimal number.` };
    if (field.kind === "rate") {
      const rate = Number(`${shifted.whole}${shifted.fraction ? `.${shifted.fraction}` : ""}`);
      return Number.isFinite(rate) && rate > 0 ? { value: rate } : { error: t`Must be finite and above 0.` };
    }
    if (shifted.fraction) return { error: t`Use at most six decimal places (whole bytes).` };
    return shifted.whole <= 0xffffffffffffffffn ? { value: shifted.whole } : { error: t`Must be at most 18446744073709.551615 MB.` };
  }
  const number = Number(text);
  if (field.kind === "wraps")
    return Number.isInteger(number) && number > 0 && number <= 0xffffffff ? { value: number } : { error: t`Must be a whole number between 1 and 4294967295.` };
  if (!Number.isFinite(number) || Math.abs(number) >= maxDurationSeconds) return { error: t`Must be a finite number of seconds below 9223372036.` };
  if (number < 0) return { error: t`Must be nonnegative.` };
  if (field.kind === "positiveSeconds" && number <= 0) return { error: t`Must be above 0.` };
  return { value: number };
};

/** The editable text of one read parameter set. */
export type ReadParametersForm = {
  density: string;
  driveSerial: string;
  sourceReference: string;
  values: Record<string, string>;
};

export const readParametersForm = (value: TapeReadParameters): ReadParametersForm => ({
  density: value.appliesTo?.densityCode === undefined ? "" : String(value.appliesTo.densityCode),
  driveSerial: value.appliesTo?.driveSerial ?? "",
  sourceReference: value.sourceReference,
  values: Object.fromEntries(readParameterFields.map((field) => [field.key, formatText(field, storedValue(value, field))])),
});

/** Inline field errors, keyed like the form values plus "density". */
export const readParametersFormErrors = (form: ReadParametersForm, usedDensities: number[]): Record<string, string> => {
  const errors: Record<string, string> = {};
  const density = parseDensityCode(form.density);
  if (!form.density.trim()) errors.density = t`Choose a density code.`;
  else if (Number.isNaN(density)) errors.density = t`Enter a code from 0 to 255, in decimal or as 0x hexadecimal.`;
  else if (usedDensities.includes(density)) {
    const label = densityLabel(density);
    errors.density = t`This device already has read parameters for ${label}; edit that set instead.`;
  }
  for (const field of readParameterFields) {
    const { error } = parseText(field, form.values[field.key]);
    if (error) errors[field.key] = error;
  }
  return errors;
};

// compact drops a sub-message whose fields are all absent and that carries nothing else.
const compact = <T extends object>(value: T): T | undefined =>
  Object.values(value).some((entry) => entry !== undefined) ||
  Object.getOwnPropertySymbols(value).some((key) => Object.prototype.propertyIsEnumerable.call(value, key))
    ? value
    : undefined;

/**
 * Applies the form to the original set. A field whose text is unchanged keeps its stored value
 * exactly, and everything the form does not show, including unknown fields, is carried over.
 */
export const applyReadParametersForm = (original: TapeReadParameters, initial: ReadParametersForm, form: ReadParametersForm): TapeReadParameters => {
  const next: TapeReadParameters = { ...original };
  const sections: Record<Section, Record<string, Stored>> = {
    positioning: { ...original.positioning },
    reading: { ...original.reading },
    completion: { ...original.completion },
    geometry: { ...original.geometry },
  };
  for (const field of readParameterFields) {
    if (form.values[field.key] === initial.values[field.key]) continue;
    sections[field.section][field.field] = parseText(field, form.values[field.key]).value;
  }
  next.positioning = compact(sections.positioning) as TapeReadParameters["positioning"];
  next.reading = compact(sections.reading) as TapeReadParameters["reading"];
  next.completion = compact(sections.completion) as TapeReadParameters["completion"];
  next.geometry = compact(sections.geometry) as TapeReadParameters["geometry"];
  const appliesTo = { ...original.appliesTo };
  if (form.density !== initial.density) appliesTo.densityCode = parseDensityCode(form.density);
  if (form.driveSerial !== initial.driveSerial) appliesTo.driveSerial = form.driveSerial.trim() ? form.driveSerial.trim() : undefined;
  next.appliesTo = appliesTo;
  if (form.sourceReference !== initial.sourceReference) next.sourceReference = form.sourceReference.trim();
  return next;
};

/** Compact display of a stored value for the parameter table. */
export const formatSeconds = (value?: number) => {
  if (value === undefined) return t`Unknown`;
  const seconds = value.toFixed(2);
  return t`${seconds} s`;
};
export const formatRate = (value?: number) => {
  if (value === undefined) return t`Unknown`;
  const rate = (value / 1e6).toFixed(1);
  return t`${rate} MB/s`;
};
