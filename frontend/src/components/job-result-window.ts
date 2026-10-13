/** Bounded sparse rows shared by every Job result view. Positions refer to the full manifest. */
export const resultPageSize = 200;
export const resultCacheSize = resultPageSize * 6;
export type ResultWindow = { start: number; rows: unknown[] };
export const windowEnd = (window: ResultWindow) => window.start + window.rows.length;

export const loadedAt = (windows: ResultWindow[], index: number): unknown => {
  const window = windows.find((window) => index >= window.start && index < windowEnd(window));
  return window?.rows[index - window.start];
};

export const firstUncovered = (windows: ResultWindow[], start: number, end: number) => {
  let index = start;
  for (const window of windows) {
    if (window.start > index || index > end) break;
    index = Math.max(index, windowEnd(window));
  }
  return Math.min(index, end + 1);
};

/** Retain nearby pages, never an ever-growing copy of all previously visited rows. */
export const mergeWindow = (windows: ResultWindow[], incoming: ResultWindow, visibleStart: number): ResultWindow[] => {
  const start = Math.max(0, visibleStart - resultPageSize * 2);
  const end = start + resultCacheSize;
  const merged: ResultWindow[] = [];
  for (const window of [...windows, incoming].sort((left, right) => left.start - right.start)) {
    const from = Math.max(start, window.start);
    const to = Math.min(end, windowEnd(window));
    if (from >= to) continue;
    const last = merged.at(-1);
    if (last && from <= windowEnd(last)) {
      last.rows.push(...window.rows.slice(Math.max(from, windowEnd(last)) - window.start, to - window.start));
    } else {
      merged.push({ start: from, rows: window.rows.slice(from - window.start, to - window.start) });
    }
  }
  return merged;
};

export const planAnchor = (cursorOf: (row: any) => string, windows: ResultWindow[], index: number) => {
  const after = windows.find((window) => windowEnd(window) <= index && index - windowEnd(window) < resultPageSize);
  if (after) return { cursor: cursorOf(after.rows.at(-1)), backwards: false, start: () => windowEnd(after) };
  const before = windows.find((window) => window.start > index && window.start - index <= resultPageSize);
  if (before) return { cursor: cursorOf(before.rows[0]), backwards: true, start: (rows: number) => before.start - rows };
  return { cursor: "", backwards: false, offset: index === 0 ? undefined : BigInt(index), start: () => index };
};
