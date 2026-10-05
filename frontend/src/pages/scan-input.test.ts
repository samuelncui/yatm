import { describe, expect, it, beforeEach } from "vitest";
import { FileScope, FileSelection, LocationSelection, PreviewPolicy, ScanResultPolicy as Result, ScanSignaturePolicy as Signature } from "@/entity";
import { scanPreferencesKey } from "./scan-preferences";
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

beforeEach(() => localStorage.clear());

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
    const initial = initialScan(new URLSearchParams("collect=1"), { selections: [selection] });

    expect(initial.target).toEqual(expect.objectContaining({ kind: "files" }));
    expect(initial.options).toEqual({
      signaturePolicy: Signature.KNOWN_ONLY,
      resultPolicy: Result.PUBLISH_ORIGINALS,
      compare: false,
      previewPolicy: PreviewPolicy.NONE,
    });
  });

  it("lets an explicit unified shortcut override saved and legacy collection options", () => {
    localStorage.setItem(
      scanPreferencesKey,
      JSON.stringify({
        kind: "location",
        location: { id: "1", rootPath: "/photos" },
        signaturePolicy: Signature.FILL_MISSING,
        resultPolicy: Result.PUBLISH_ORIGINALS,
        compare: true,
        previewPolicy: PreviewPolicy.NONE,
      }),
    );
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

    expect(initialScan(new URLSearchParams("collect=1&result=verify&previews=1"), state)).toMatchObject({
      target: state.scan.target,
      options: state.scan.options,
      explicit: true,
    });
  });

  it("uses a URL target while retaining remembered editable options", () => {
    localStorage.setItem(
      scanPreferencesKey,
      JSON.stringify({
        kind: "location",
        location: { id: "1", rootPath: "/photos" },
        signaturePolicy: Signature.KNOWN_ONLY,
        resultPolicy: Result.REPORT_ONLY,
        compare: false,
        previewPolicy: PreviewPolicy.MISSING_ONLY,
      }),
    );

    expect(initialScan(new URLSearchParams("media=3"), null)).toMatchObject({
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
  ])("blocks %s instead of restoring a remembered whole target", (_, params, state) => {
    localStorage.setItem(
      scanPreferencesKey,
      JSON.stringify({
        kind: "location",
        location: { id: "1", rootPath: "/photos" },
        signaturePolicy: Signature.FILL_MISSING,
        resultPolicy: Result.PUBLISH_ORIGINALS,
        compare: true,
        previewPolicy: PreviewPolicy.NONE,
      }),
    );

    const initial = initialScan(params, state);
    expect(initial.error).not.toBe("");
    expect(initial.target).toEqual({ kind: "files", entries: [] });
    expect(initial.explicit).toBe(true);
  });
});
