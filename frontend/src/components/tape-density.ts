import { t } from "@lingui/core/macro";
/** The standard LTO density codes the UI can name; any other code shows as the code alone. */
export const tapeDensityNames: ReadonlyMap<number, string> = new Map([
  [0x40, "LTO-1"],
  [0x42, "LTO-2"],
  [0x44, "LTO-3"],
  [0x46, "LTO-4"],
  [0x58, "LTO-5"],
  [0x5a, "LTO-6"],
  [0x5c, "LTO-7"],
  [0x5d, "LTO-M8"],
  [0x5e, "LTO-8"],
  [0x60, "LTO-9"],
]);

export const densityLabel = (code?: number): string => {
  if (code === undefined) return t`Unknown density`;
  const hex = `0x${code.toString(16).toUpperCase().padStart(2, "0")}`;
  const name = tapeDensityNames.get(code);
  return name ? `${hex} ${name}` : hex;
};
