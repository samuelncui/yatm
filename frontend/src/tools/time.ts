export const minUnixNs = -(1n << 63n);
export const maxUnixNs = (1n << 63n) - 1n;
const nsPerMs = 1_000_000n;

export const isUnixNs = (value: bigint) => value >= minUnixNs && value <= maxUnixNs;

export function parseUnixNs(value: string): bigint | undefined {
  if (!/^-?\d+$/.test(value)) return undefined;
  const instant = BigInt(value);
  return isUnixNs(instant) ? instant : undefined;
}

/** Date is a display boundary only; keep the original bigint for requests and ordering. */
export function dateFromNs(value?: bigint): Date | undefined {
  if (value === undefined || !isUnixNs(value)) return undefined;
  // Floor negative instants too: -1 ns belongs to the millisecond before the epoch.
  const ms = value / nsPerMs - (value < 0n && value % nsPerMs !== 0n ? 1n : 0n);
  return new Date(Number(ms));
}

export function dateToNs(date: Date): bigint | undefined {
  const ms = date.getTime();
  if (!Number.isSafeInteger(ms)) return undefined;
  const value = BigInt(ms) * nsPerMs;
  return isUnixNs(value) ? value : undefined;
}
