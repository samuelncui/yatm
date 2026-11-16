import { describe, expect, it } from "vitest";
import { FileScope, FileSelection, LocationSelection, PreviewPolicy, ScanResultPolicy as Result, ScanSignaturePolicy as Signature } from "@/entity";
import type { LastScan } from "./scan-preferences";
import { initialScan } from "./scan-input";

const selection = FileSelection.create({
  target: {
    oneofKind: "location",
    location: LocationSelection.create({
      locationId: 1n,
      path: "Projects",
    }),
  },
  scope: FileScope.ALL,
});
// The most recently created Scan read a whole Location.
const last: LastScan = {
  kind: "location",
  id: "1",
  signaturePolicy: Signature.KNOWN_ONLY,
  resultPolicy: Result.REPORT_ONLY,
  compare: false,
  previewPolicy: PreviewPolicy.MISSING_ONLY,
  writeSignatureCache: true,
};

describe("initialScan", () => {
  it("keeps a routed Files target and its editable policies", () => {
    const state = {
      scan: {
        target: { kind: "files" as const, entries: [{ selection, name: "Projects", path: "/photos/Projects", isDir: true }] },
        options: {
          signaturePolicy: Signature.KNOWN_ONLY,
          resultPolicy: Result.REPORT_ONLY,
          compare: false,
          previewPolicy: PreviewPolicy.MISSING_ONLY,
        },
      },
    };

    expect(initialScan(new URLSearchParams(), state)).toMatchObject({
      target: state.scan.target,
      options: state.scan.options,
      explicit: true,
      error: "",
    });
  });

  it("treats existing collection shortcuts as a policy prefill, not a separate mode", () => {
    const initial = initialScan(new URLSearchParams("collect=1"), null);

    expect(initial.target).toEqual({ kind: "files", entries: [] });
    expect(initial.options).toEqual({
      signaturePolicy: Signature.KNOWN_ONLY,
      resultPolicy: Result.PUBLISH_ORIGINALS,
      compare: false,
      previewPolicy: PreviewPolicy.NONE,
      writeSignatureCache: true,
    });
  });

  it("starts from built-in defaults when no Scan was created before", () => {
    expect(initialScan(new URLSearchParams(), null)).toEqual({
      target: { kind: "location", id: "" },
      options: {
        signaturePolicy: Signature.FILL_MISSING,
        resultPolicy: Result.PUBLISH_ORIGINALS,
        compare: true,
        previewPolicy: PreviewPolicy.NONE,
        writeSignatureCache: true,
      },
      error: "",
      explicit: false,
    });
  });

  it("starts from the last Scan's source and options", () => {
    const { kind, id, ...options } = last;
    expect(initialScan(new URLSearchParams(), null, last)).toEqual({ target: { kind, id }, options, error: "", explicit: false });
    expect(initialScan(new URLSearchParams(), null, { ...last, writeSignatureCache: false }).options.writeSignatureCache).toBe(false);
    // A last Scan of selected files leaves the Location to choose.
    expect(initialScan(new URLSearchParams(), null, { ...last, id: "" }).target).toEqual({ kind: "location", id: "" });
  });

  it("keeps a last Media source and maps a Location result policy to that source", () => {
    const initial = initialScan(new URLSearchParams(), null, { ...last, kind: "media", id: "3", resultPolicy: Result.PUBLISH_ORIGINALS });
    expect(initial.target).toEqual({ kind: "media", id: "3" });
    expect(initial.options.resultPolicy).toBe(Result.PUBLISH_INVENTORY);
  });

  it("lets an explicit unified shortcut override the last Scan and legacy collection options", () => {
    const state = {
      scan: {
        target: { kind: "files" as const, entries: [{ selection, name: "Projects", path: "/photos/Projects", isDir: true }] },
        options: {
          signaturePolicy: Signature.FORCE_READ,
          resultPolicy: Result.REPORT_ONLY,
          compare: false,
          previewPolicy: PreviewPolicy.REGENERATE_ALL,
        },
      },
    };

    expect(initialScan(new URLSearchParams("collect=1&result=verify&previews=1"), state, last)).toMatchObject({
      target: state.scan.target,
      options: state.scan.options,
      explicit: true,
    });
  });

  it("uses a URL target while retaining the last Scan's editable options", () => {
    expect(initialScan(new URLSearchParams("media=3"), null, last)).toMatchObject({
      target: { kind: "media", id: "3" },
      options: {
        signaturePolicy: Signature.KNOWN_ONLY,
        resultPolicy: Result.REPORT_ONLY,
        compare: false,
        previewPolicy: PreviewPolicy.MISSING_ONLY,
      },
      explicit: true,
    });
  });

  it.each([
    ["both URL sources", new URLSearchParams("location=1&media=3"), null],
    ["a malformed routed Files entry", new URLSearchParams(), { scan: { target: { kind: "files", entries: [null] } } }],
    ["an invalid Location id", new URLSearchParams(), { scan: { target: { kind: "location", id: "0" } } }],
    ["an invalid policy", new URLSearchParams(), { scan: { options: { compare: "yes" } } }],
  ])("blocks %s instead of starting from the last Scan's source", (_, params, state) => {
    const initial = initialScan(params, state, last);
    expect(initial.error).not.toBe("");
    expect(initial.target).toEqual({ kind: "files", entries: [] });
    expect(initial.explicit).toBe(true);
  });
});
