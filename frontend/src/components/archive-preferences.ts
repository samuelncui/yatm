/** Parses a target Media count input: a whole number of at least one, or undefined. */
export function parseTargetMediaCount(text: string): number | undefined {
  const value = text.trim();
  if (!/^[1-9]\d{0,9}$/.test(value) || Number(value) > 0xffffffff) return undefined;
  return Number(value);
}
