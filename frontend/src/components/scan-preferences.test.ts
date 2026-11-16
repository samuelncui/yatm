import { describe, expect, it } from "vitest";
import { FileScope, FileSelection, PreviewPolicy, ScanJobSpec, ScanResultPolicy as Result, ScanSignaturePolicy as Signature } from "@/entity";
import { lastScan, resultForSource } from "./scan-preferences";

const location = (path = "", scope = FileScope.ALL, locationId = 9007199254740993n) =>
  FileSelection.create({ target: { oneofKind: "location", location: { locationId, path } }, scope });
const options = {
  signaturePolicy: Signature.KNOWN_ONLY,
  resultPolicy: Result.REPORT_ONLY,
  compareLibrary: false,
  previewPolicy: PreviewPolicy.MISSING_ONLY,
  writeSignatureCache: false,
};

describe("lastScan", () => {
  it("takes the Media source and every option from a Media Scan", () => {
    expect(lastScan(ScanJobSpec.create({ ...options, mediaId: 3n }))).toEqual({
      kind: "media",
      id: "3",
      signaturePolicy: Signature.KNOWN_ONLY,
      resultPolicy: Result.REPORT_ONLY,
      compare: false,
      previewPolicy: PreviewPolicy.MISSING_ONLY,
      writeSignatureCache: false,
    });
  });

  it("takes a whole Location from its single root selection without losing a large ID", () => {
    expect(lastScan(ScanJobSpec.create({ ...options, selections: [location()] }))).toMatchObject({ kind: "location", id: "9007199254740993", compare: false });
  });

  it.each([
    ["no selection", []],
    ["a folder inside a Location", [location("Projects")]],
    ["a narrower scope", [location("", FileScope.SAVED)]],
    ["several selections", [location(), location("", FileScope.ALL, 2n)]],
    ["a Library selection", [FileSelection.create({ target: { oneofKind: "library", library: { fileId: 7n } }, scope: FileScope.ALL })]],
  ])("leaves the Location unset after a Scan of %s while keeping its options", (_, selections) => {
    expect(lastScan(ScanJobSpec.create({ ...options, selections }))).toMatchObject({ kind: "location", id: "", signaturePolicy: Signature.KNOWN_ONLY });
  });

  it("writes the signature cache when the Scan was created before that choice existed", () => {
    expect(lastScan(ScanJobSpec.create({ mediaId: 3n })).writeSignatureCache).toBe(true);
    expect(lastScan(ScanJobSpec.create({ mediaId: 3n, writeSignatureCache: true })).writeSignatureCache).toBe(true);
  });
});

describe("resultForSource", () => {
  it.each([
    ["location", undefined, Result.PUBLISH_ORIGINALS],
    ["files", Result.PUBLISH_INVENTORY, Result.PUBLISH_ORIGINALS],
    ["location", Result.VERIFY_COPIES, Result.PUBLISH_ORIGINALS],
    ["location", Result.REPORT_ONLY, Result.REPORT_ONLY],
    ["media", undefined, Result.PUBLISH_INVENTORY],
    ["media", Result.PUBLISH_ORIGINALS, Result.PUBLISH_INVENTORY],
    ["media", Result.VERIFY_COPIES, Result.VERIFY_COPIES],
    ["media", Result.REPORT_ONLY, Result.REPORT_ONLY],
  ])("maps a %s source with result %s to %s", (kind, result, expected) => {
    expect(resultForSource(kind, result)).toBe(expected);
  });
});
