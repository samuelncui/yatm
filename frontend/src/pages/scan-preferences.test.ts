import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MediaKind, PreviewPolicy, ScanResultPolicy, ScanSignaturePolicy } from "@/entity";
import { loadScanPreferences, saveScanPreferences, scanPreferencesKey, type ScanPreferences } from "./scan-preferences";

const preferences: ScanPreferences = {
  kind: "location",
  location: { id: "9007199254740993", rootPath: "/photos" },
  media: { id: "3", kind: MediaKind.VOLUME, identity: "disk" },
  signaturePolicy: ScanSignaturePolicy.KNOWN_ONLY,
  resultPolicy: ScanResultPolicy.REPORT_ONLY,
  compare: false,
  previewPolicy: PreviewPolicy.NONE,
};

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});
afterEach(() => vi.restoreAllMocks());

it("round-trips the existing local preference shape without rewriting it during a read", () => {
  saveScanPreferences(preferences);
  expect(localStorage.getItem(scanPreferencesKey)).toBe(JSON.stringify(preferences));
  expect(sessionStorage.getItem(scanPreferencesKey)).toBeNull();
  const write = vi.spyOn(Storage.prototype, "setItem");
  expect(loadScanPreferences()).toEqual(preferences);
  expect(write).not.toHaveBeenCalled();
});

it("keeps an absent preference unknown instead of saving defaults", () => {
  const write = vi.spyOn(Storage.prototype, "setItem");
  expect(loadScanPreferences()).toBeUndefined();
  expect(write).not.toHaveBeenCalled();
});

it.each([
  ["malformed JSON", "{"],
  ["null", "null"],
  ["unknown source", JSON.stringify({ ...preferences, kind: "files" })],
  ["invalid comparison", JSON.stringify({ ...preferences, compare: "yes" })],
  ["invalid signature policy", JSON.stringify({ ...preferences, signaturePolicy: -1 })],
  ["invalid result policy", JSON.stringify({ ...preferences, resultPolicy: -1 })],
  ["invalid Preview policy", JSON.stringify({ ...preferences, previewPolicy: -1 })],
  ["invalid Location", JSON.stringify({ ...preferences, location: { id: "0", rootPath: "/photos" } })],
  ["invalid Media", JSON.stringify({ ...preferences, media: { id: "3", kind: -1, identity: "disk" } })],
])("ignores %s without changing the stored value", (_, raw) => {
  localStorage.setItem(scanPreferencesKey, raw);
  expect(loadScanPreferences()).toBeUndefined();
  expect(localStorage.getItem(scanPreferencesKey)).toBe(raw);
});

it.each(["getItem", "setItem"] as const)("contains a denied %s without changing the operation outcome", (method) => {
  vi.spyOn(Storage.prototype, method).mockImplementation(() => {
    throw new Error("Storage denied");
  });
  if (method === "getItem") expect(loadScanPreferences()).toBeUndefined();
  else expect(() => saveScanPreferences(preferences)).not.toThrow();
});

it("contains failure to access localStorage itself", () => {
  vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
    throw new Error("Storage unavailable");
  });
  expect(loadScanPreferences()).toBeUndefined();
  expect(() => saveScanPreferences(preferences)).not.toThrow();
});
