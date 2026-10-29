import { bench, expect } from "vitest";
import { mergeWindow, resultCacheSize, type ResultWindow } from "./job-result-window";

const cases = [1_000, 10_000, 100_000, 1_000_000].map((totalRows) => {
  const rows = Array.from({ length: totalRows }, (_, index) => ({ id: index + 1 }));
  const pages: ResultWindow[] = [];
  for (let start = 0; start < totalRows; start += 200) {
    pages.push({ start, rows: rows.slice(start, start + 200) });
  }
  const lastPageStart = pages.at(-1)!.start;
  const expectedStart = Math.max(0, lastPageStart - 400);
  const expectedCount = Math.min(totalRows - expectedStart, resultCacheSize);
  return { totalRows, pages, rows, expectedStart, expectedCount };
});

for (const { totalRows, pages, rows, expectedStart, expectedCount } of cases) {
  let windows: ResultWindow[] = [];
  bench(
    `append ${totalRows} Job result rows in 200-row windows`,
    () => {
      windows = [];
      for (const page of pages) windows = mergeWindow(windows, page, page.start);
    },
    {
      time: 250,
      iterations: 1,
      warmupTime: 50,
      warmupIterations: 1,
      // Native task teardown checks the final operation outside the measured loop.
      teardown: () => {
        const retained = windows.flatMap((window) => window.rows);
        expect(retained.length).toBe(expectedCount);
        expect(retained.length).toBeLessThanOrEqual(resultCacheSize);
        expect(windows[0]?.start).toBe(expectedStart);
        expect(retained.every((row, index) => row === rows[expectedStart + index])).toBe(true);
      },
    },
  );
}
