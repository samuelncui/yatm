import { TapeReadParameters, TapeRecordMode, type InspectDeviceResponse } from "@/entity";

export const validateTapeReadParameters = (value?: TapeReadParameters): string => {
  if (!value) return "";
  const scope = value.appliesTo;
  if (scope) {
    for (const [name, text] of [
      ["drive serial", scope.driveSerial],
      ["firmware", scope.firmware],
    ]) {
      if (text !== undefined && !text.trim()) return `Read parameters ${name} must be nonempty when supplied.`;
    }
    if (scope.densityCode !== undefined && (!Number.isInteger(scope.densityCode) || scope.densityCode < 0 || scope.densityCode > 255))
      return "Read parameters density code must be between 0 and 255.";
    if (scope.recordMode !== undefined && ![TapeRecordMode.FIXED, TapeRecordMode.VARIABLE].includes(scope.recordMode))
      return "Read parameters record mode must be Fixed or Variable.";
    if (
      scope.recordBytes !== undefined &&
      (!Number.isInteger(scope.recordBytes) || scope.recordBytes <= 0 || scope.recordBytes > 0xffffffff || scope.recordBytes % 512 !== 0)
    )
      return "Read parameters record bytes must be a positive uint32 multiple of 512.";
    if (scope.encryptionAlgorithm !== undefined && scope.encryptionAlgorithm !== "" && !scope.encryptionAlgorithm.trim())
      return "Read parameters encryption algorithm must be empty for plaintext or name an algorithm.";
  }
  const geometry = value.geometry;
  if (geometry) {
    if (geometry.nominalWraps !== undefined && (!Number.isInteger(geometry.nominalWraps) || geometry.nominalWraps <= 0 || geometry.nominalWraps > 0xffffffff))
      return "Read parameters nominal wraps must be a positive uint32.";
    for (const bound of [geometry.minLpos, geometry.maxLpos]) {
      if (bound !== undefined && (bound < 0n || bound > 0xffffffffffffffffn)) return "Read parameters LPOS bounds must be uint64 values.";
    }
    if (geometry.minLpos !== undefined && geometry.maxLpos !== undefined && geometry.maxLpos <= geometry.minLpos)
      return "Read parameters maximum LPOS must exceed minimum LPOS.";
  }
  const positioning = value.positioning;
  const reading = value.reading;
  const completion = value.completion;
  if (completion?.fullSpanSeconds !== undefined && completion.fullSpanSeconds <= 0) return "Read parameters full-span unload seconds must be positive.";
  if (positioning?.longitudinalSecondsPerLpos !== undefined && positioning.longitudinalSecondsPerWrap !== undefined)
    return "Read parameters longitudinal units must be either seconds per LPOS or seconds per wrap.";
  const seconds: [string, number | undefined, boolean][] = [
    ["base", positioning?.baseSeconds, true],
    ["wrap change", positioning?.wrapChangeSeconds, true],
    ["band change", positioning?.bandChangeSeconds, true],
    ["landing zone change", positioning?.landingZoneChangeSeconds, true],
    ["direction change", positioning?.directionChangeSeconds, true],
    ["step back", positioning?.stepBackSeconds, true],
    ["longitudinal seconds per LPOS", positioning?.longitudinalSecondsPerLpos, false],
    ["longitudinal seconds per wrap", positioning?.longitudinalSecondsPerWrap, false],
    ["post-LOCATE READ overhead", reading?.postLocateReadOverheadSeconds, false],
    ["filemark overhead", reading?.filemarkOverheadSeconds, false],
    ["full-span unload", completion?.fullSpanSeconds, false],
    ["fixed unload", completion?.fixedSeconds, false],
  ];
  for (const [name, number, signed] of seconds) {
    if (number === undefined) continue;
    if (!Number.isFinite(number) || Math.abs(number) >= 9223372036.854776) return `Read parameters ${name} must be finite and representable as a duration.`;
    if (!signed && number < 0) return `Read parameters ${name} must be nonnegative.`;
  }
  if (reading?.bytesPerSecond !== undefined && (!Number.isFinite(reading.bytesPerSecond) || reading.bytesPerSecond <= 0))
    return "Read parameters bytes per second must be finite and positive.";
  return "";
};

export const importTapeReadParameters = (text: string): TapeReadParameters => {
  const value = TapeReadParameters.fromJsonString(text, { ignoreUnknownFields: false });
  const error = validateTapeReadParameters(value);
  if (error) throw new Error(error);
  return value;
};

export const captureTapeReadConditions = (report: InspectDeviceResponse): TapeReadParameters => {
  const tape = report.report.oneofKind === "tape" ? report.report.tape : undefined;
  return TapeReadParameters.create({
    appliesTo: {
      driveSerial: report.serialNumber?.trim() ? report.serialNumber : undefined,
      firmware: report.firmware?.trim() ? report.firmware : undefined,
      densityCode: tape?.densityCode,
      recordMode: tape?.fixedRecords === undefined ? undefined : tape.fixedRecords ? TapeRecordMode.FIXED : TapeRecordMode.VARIABLE,
      // Variable mode reports a zero block size, not its writer's maximum record size.
      recordBytes: tape?.fixedRecords === true && tape.recordBytes ? tape.recordBytes : undefined,
    },
  });
};
