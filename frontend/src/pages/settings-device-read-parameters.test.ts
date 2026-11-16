import { describe, expect, it } from "vitest";
import { InspectDeviceResponse, TapeDeviceReport, TapeReadParameters } from "@/entity";
import {
  applyReadParametersForm,
  captureTapeReadConditions,
  densityLabel,
  parseDensityCode,
  readParametersForm,
  readParametersFormErrors,
  validateTapeReadParameters,
  validateTapeReadParameterSets,
} from "./settings-device-read-parameters";

const density = { appliesTo: { densityCode: 88 } };

describe("read parameter validation", () => {
  it("requires a unique density code per device", () => {
    const first = TapeReadParameters.create(density);
    const other = TapeReadParameters.create({ appliesTo: { densityCode: 90, driveSerial: "SN1" } });
    expect(validateTapeReadParameterSets([first, other])).toBe("");
    expect(validateTapeReadParameterSets([first, other, first])).toBe("Read parameters for density code 88 are repeated.");
    expect(validateTapeReadParameters(TapeReadParameters.create())).toBe("Read parameters density code is required.");
  });

  it.each([
    { appliesTo: { densityCode: 256 } },
    { appliesTo: { densityCode: 88, driveSerial: " " } },
    { geometry: { nominalWraps: 0 } },
    { positioning: { baseSeconds: NaN } },
    { positioning: { baseSeconds: 1e20 } },
    { positioning: { approachReversalSeconds: -1 } },
    { positioning: { seekSecondsPerWrap: Infinity } },
    { positioning: { bufferBytes: -1n } },
    { reading: { bytesPerSecond: 0 } },
    { reading: { filemarkOverheadSeconds: -1 } },
    { completion: { fullSpanSeconds: 0 } },
    { completion: { fixedSeconds: -1 } },
    { completion: { ltfsUnmountSeconds: NaN } },
  ])("rejects invalid supplied fields: %o", (input) => {
    expect(validateTapeReadParameters(TapeReadParameters.create({ ...density, ...input }))).not.toBe("");
  });
});

describe("read parameter form", () => {
  it("names the standard LTO density codes and parses other codes", () => {
    expect(densityLabel(0x58)).toBe("0x58 LTO-5");
    expect(densityLabel(0x5d)).toBe("0x5D LTO-M8");
    expect(densityLabel(0x99)).toBe("0x99");
    expect(parseDensityCode("0x5a")).toBe(90);
    expect(parseDensityCode("88")).toBe(88);
    expect(parseDensityCode("256")).toBeNaN();
    expect(parseDensityCode("x")).toBeNaN();
  });

  it("shows bytes in decimal MB and stores edited values in bytes without touching the rest", () => {
    const original = TapeReadParameters.create({
      appliesTo: { densityCode: 88, driveSerial: "SN1" },
      geometry: { nominalWraps: 80 },
      positioning: { baseSeconds: 4.291234, bufferBytes: 18446744073709551615n },
      reading: { bytesPerSecond: 93123456.7 },
      completion: { fullSpanSeconds: 107 },
      sourceReference: "Calibration Job 12",
    });
    const initial = readParametersForm(original);
    expect(initial.values.bufferBytes).toBe("18446744073709.551615");
    expect(initial.values.bytesPerSecond).toBe("93.1234567");
    expect(initial.density).toBe("88");
    // Unchanged text keeps every stored value exactly.
    expect(applyReadParametersForm(original, initial, initial)).toEqual(original);
    const edited = applyReadParametersForm(original, initial, {
      ...initial,
      values: { ...initial.values, bufferBytes: "32.5", bytesPerSecond: "140", baseSeconds: "", filemarkOverheadSeconds: "0" },
    });
    expect(edited.positioning).toEqual({ bufferBytes: 32500000n });
    expect(edited.reading).toEqual({ bytesPerSecond: 140000000, filemarkOverheadSeconds: 0 });
    expect(edited.geometry).toEqual({ nominalWraps: 80 });
    expect(edited.completion).toEqual({ fullSpanSeconds: 107 });
    expect(edited.appliesTo).toEqual({ densityCode: 88, driveSerial: "SN1" });
    expect(edited.sourceReference).toBe("Calibration Job 12");
  });

  it("carries fields the form does not show, including unknown wire fields", () => {
    const original = TapeReadParameters.fromBinary(
      new Uint8Array([...TapeReadParameters.toBinary(TapeReadParameters.create({ ...density, positioning: { baseSeconds: 4 } })), 0xf8, 0x06, 0x01]),
    );
    const marker = Symbol.for("protobuf-ts/unknown");
    expect((original as unknown as Record<symbol, unknown>)[marker]).toBeDefined();
    const initial = readParametersForm(original);
    const edited = applyReadParametersForm(original, initial, { ...initial, values: { ...initial.values, baseSeconds: "5" } });
    expect(edited.positioning?.baseSeconds).toBe(5);
    expect(TapeReadParameters.toBinary(edited)).toEqual(
      new Uint8Array([...TapeReadParameters.toBinary(TapeReadParameters.create({ ...density, positioning: { baseSeconds: 5 } })), 0xf8, 0x06, 0x01]),
    );
  });

  it("starts a new set empty and keeps missing values absent", () => {
    const initial = readParametersForm(TapeReadParameters.create());
    expect(Object.values(initial.values).every((text) => text === "")).toBe(true);
    const added = applyReadParametersForm(TapeReadParameters.create(), initial, { ...initial, density: "0x58", driveSerial: " SN2 " });
    expect(added).toEqual(TapeReadParameters.create({ appliesTo: { densityCode: 88, driveSerial: "SN2" } }));
    expect(added.positioning).toBeUndefined();
  });

  it.each([
    ["density", "", "Choose a density code."],
    ["density", "300", "Enter a code from 0 to 255"],
    ["density", "90", "already has read parameters for 0x5A LTO-6"],
    ["baseSeconds", "-1", "Must be nonnegative."],
    ["baseSeconds", "abc", "Must be a finite number of seconds"],
    ["seekSecondsPerWrap", "1e20", "Must be a finite number of seconds"],
    ["fullSpanSeconds", "0", "Must be above 0."],
    ["bytesPerSecond", "0", "Must be finite and above 0."],
    ["bytesPerSecond", "-5", "Enter a nonnegative decimal number."],
    ["bufferBytes", "1.0000001", "Use at most six decimal places"],
    ["bufferBytes", "18446744073710", "Must be at most"],
    ["nominalWraps", "80.5", "Must be a whole number"],
    ["nominalWraps", "0", "Must be a whole number"],
  ])("rejects %s %j inline", (key, text, message) => {
    const form = readParametersForm(TapeReadParameters.create(density));
    const next = key === "density" ? { ...form, density: text } : { ...form, values: { ...form.values, [key]: text } };
    const errors = readParametersFormErrors(next, [90]);
    expect(errors[key]).toContain(message);
    expect(Object.keys(errors)).toEqual([key]);
  });

  it("accepts zero where the server does and an empty form apart from the density code", () => {
    const form = readParametersForm(TapeReadParameters.create(density));
    expect(readParametersFormErrors(form, [])).toEqual({});
    expect(readParametersFormErrors({ ...form, values: { ...form.values, baseSeconds: "0", bufferBytes: "0", fixedSeconds: "0" } }, [])).toEqual({});
  });

  it("starts new parameters from the observed drive serial and density without supplying measurements", () => {
    const report = InspectDeviceResponse.create({
      serialNumber: "observed",
      firmware: "FW",
      report: {
        oneofKind: "tape",
        tape: TapeDeviceReport.create({
          densityCode: 0,
          fixedRecords: false,
          recordBytes: 0,
          compressionEnabled: true,
          encryptionEnabled: false,
        }),
      },
    });
    const captured = captureTapeReadConditions(report);
    expect(captured.appliesTo).toEqual({ driveSerial: "observed", densityCode: 0 });
    expect(captured.geometry).toBeUndefined();
    expect(captured.positioning).toBeUndefined();
    expect(captured.reading).toBeUndefined();
    expect(captured.completion).toBeUndefined();
    expect(captured.sourceReference).toBe("");
    const unknown = captureTapeReadConditions(InspectDeviceResponse.create());
    expect(unknown.appliesTo?.driveSerial).toBeUndefined();
    expect(unknown.appliesTo?.densityCode).toBeUndefined();
    expect(unknown.completion).toBeUndefined();
  });
});
