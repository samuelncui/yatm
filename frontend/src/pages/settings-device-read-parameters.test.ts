import { describe, expect, it } from "vitest";
import { InspectDeviceResponse, TapeDeviceReport, TapeReadParameters, TapeRecordMode } from "@/entity";
import { captureTapeReadConditions, importTapeReadParameters, validateTapeReadParameters } from "./settings-device-read-parameters";

describe("read parameter inputs", () => {
  it("preserves unknown values, explicit zero/false/plaintext and full-width LPOS", () => {
    const value = importTapeReadParameters(
      JSON.stringify({
        appliesTo: { densityCode: 0, hardwareCompression: false, encryptionAlgorithm: "" },
        geometry: { minLpos: "0", maxLpos: "18446744073709551615" },
        positioning: { baseSeconds: 0, landingZoneChangeSeconds: -6.04 },
        reading: { bytesPerSecond: 93000000 },
        completion: { fullSpanSeconds: 107, fixedSeconds: 0 },
        sourceReference: "Sequential measurement; overhead remains unknown",
      }),
    );
    expect(value.appliesTo).toMatchObject({ densityCode: 0, hardwareCompression: false, encryptionAlgorithm: "" });
    expect(value.geometry).toEqual({ minLpos: 0n, maxLpos: 18446744073709551615n });
    expect(value.positioning?.landingZoneChangeSeconds).toBe(-6.04);
    expect(value.reading?.postLocateReadOverheadSeconds).toBeUndefined();
    expect(value.reading?.filemarkOverheadSeconds).toBeUndefined();
    expect(value.completion).toEqual({ fullSpanSeconds: 107, fixedSeconds: 0 });
    expect(importTapeReadParameters(TapeReadParameters.toJsonString(value))).toEqual(value);
    expect(validateTapeReadParameters(importTapeReadParameters("{}"))).toBe("");
  });

  it.each([
    { unknownField: 1 },
    { appliesTo: { densityCode: 256 } },
    { appliesTo: { recordMode: 99 } },
    { appliesTo: { recordBytes: 513 } },
    { geometry: { nominalWraps: 0 } },
    { geometry: { minLpos: "7", maxLpos: "7" } },
    { positioning: { baseSeconds: "NaN" } },
    { positioning: { baseSeconds: 1e20 } },
    { positioning: { longitudinalSecondsPerLpos: -1 } },
    { positioning: { longitudinalSecondsPerWrap: -1 } },
    { positioning: { longitudinalSecondsPerWrap: "NaN" } },
    { positioning: { longitudinalSecondsPerWrap: "Infinity" } },
    { positioning: { longitudinalSecondsPerLpos: 0, longitudinalSecondsPerWrap: 0 } },
    { reading: { bytesPerSecond: 0 } },
    { reading: { bytesPerSecond: "Infinity" } },
    { reading: { postLocateReadOverheadSeconds: -1 } },
    { reading: { filemarkOverheadSeconds: -1 } },
    { reading: { uncertaintySeconds: 0 } },
    ...["fullSpanSeconds", "fixedSeconds"].flatMap((field) => ["NaN", "Infinity", "-Infinity", -1, 1e20].map((value) => ({ completion: { [field]: value } }))),
    { completion: { fullSpanSeconds: 0 } },
  ])("rejects invalid supplied fields: %j", (input) => {
    expect(() => importTapeReadParameters(JSON.stringify(input))).toThrow();
  });

  it.each([0, 107])("round-trips seconds per wrap without LPOS bounds: %s", (seconds) => {
    const value = importTapeReadParameters(JSON.stringify({ positioning: { longitudinalSecondsPerWrap: seconds } }));
    expect(value.geometry).toBeUndefined();
    expect(value.positioning?.longitudinalSecondsPerWrap).toBe(seconds);
    expect(value.positioning?.longitudinalSecondsPerLpos).toBeUndefined();
    expect(importTapeReadParameters(TapeReadParameters.toJsonString(value))).toEqual(value);
  });

  it.each([{}, { fullSpanSeconds: 107 }, { fixedSeconds: 0 }])(
    "preserves partial completion inputs without manufacturing missing timings: %j",
    (completion) => {
      const value = importTapeReadParameters(JSON.stringify({ completion }));
      expect(value.completion).toEqual(completion);
      expect(JSON.parse(TapeReadParameters.toJsonString(value))).toEqual({ completion });
      expect(value.positioning).toBeUndefined();
      expect(value.reading).toBeUndefined();
      expect(importTapeReadParameters(TapeReadParameters.toJsonString(value))).toEqual(value);
    },
  );

  it("starts new parameters from observed conditions without supplying measurements or written-profile facts", () => {
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
    expect(captured.appliesTo).toEqual({
      driveSerial: "observed",
      firmware: "FW",
      densityCode: 0,
      recordMode: TapeRecordMode.VARIABLE,
      recordBytes: undefined,
    });
    expect(captured.geometry).toBeUndefined();
    expect(captured.positioning).toBeUndefined();
    expect(captured.reading).toBeUndefined();
    expect(captured.completion).toBeUndefined();
    expect(captured.sourceReference).toBe("");
    expect(captured.appliesTo?.hardwareCompression).toBeUndefined();
    expect(captured.appliesTo?.encryptionAlgorithm).toBeUndefined();
    const unknown = captureTapeReadConditions(InspectDeviceResponse.create());
    expect(unknown.appliesTo?.driveSerial).toBeUndefined();
    expect(unknown.appliesTo?.recordMode).toBeUndefined();
    expect(unknown.appliesTo?.recordBytes).toBeUndefined();
    expect(unknown.completion).toBeUndefined();
    const fixed = captureTapeReadConditions(
      InspectDeviceResponse.create({
        report: { oneofKind: "tape", tape: TapeDeviceReport.create({ fixedRecords: true, recordBytes: 4096 }) },
      }),
    );
    expect(fixed.appliesTo?.recordMode).toBe(TapeRecordMode.FIXED);
    expect(fixed.appliesTo?.recordBytes).toBe(4096);
    expect(fixed.reading).toBeUndefined();
    expect(fixed.completion).toBeUndefined();
  });
});
