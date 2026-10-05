export type Interval = { start: number; end: number };

/** Sparse rows stay in snapshot order while locally hidden rows consume no display position. */
export function mergeIntervals(intervals: Interval[]): Interval[] {
  const merged: Interval[] = [];
  for (const interval of [...intervals].sort((a, b) => a.start - b.start)) {
    const last = merged.at(-1);
    if (last && interval.start <= last.end + 1) last.end = Math.max(last.end, interval.end);
    else merged.push({ ...interval });
  }
  return merged;
}

export function displayedCount(total: number, excluded: Interval[]): number {
  return Math.max(0, total - excluded.reduce((count, interval) => count + interval.end - interval.start + 1, 0));
}

export function toDisplayPosition(position: number, excluded: Interval[]): number | undefined {
  let skipped = 0;
  for (const interval of excluded) {
    if (position < interval.start) break;
    if (position <= interval.end) return undefined;
    skipped += interval.end - interval.start + 1;
  }
  return position - skipped;
}

export function toSnapshotPosition(index: number, excluded: Interval[]): number {
  let position = index;
  for (const interval of excluded) {
    if (position < interval.start) break;
    position += interval.end - interval.start + 1;
  }
  return position;
}
