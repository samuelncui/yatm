import { bench, expect } from "vitest";
import { mergeWindow, type ResultWindow } from "./job-result-window";

const pages = Array.from({ length: 500 }, (_, page) => ({
  start: page * 200,
  rows: Array.from({ length: 200 }, (_, index) => ({ id: page * 200 + index + 1 })),
}));

bench(
  "append 100k Job result rows in 200-row windows",
  () => {
    let windows: ResultWindow[] = [];
    for (const page of pages) windows = mergeWindow(windows, page, page.start);
    expect(windows.at(-1)!.rows.at(-1)).toEqual({ id: 100_000 });
  },
  { time: 250, iterations: 1, warmupTime: 50, warmupIterations: 1 },
);
