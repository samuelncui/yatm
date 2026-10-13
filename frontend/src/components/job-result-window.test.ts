import { describe, expect, it } from "vitest";
import { firstUncovered, loadedAt, mergeWindow, planAnchor, resultCacheSize, type ResultWindow } from "./job-result-window";

const windowAt = (start: number, count = 200): ResultWindow => ({ start, rows: Array.from({ length: count }, (_, index) => start + index + 1) });

describe("Job result windows", () => {
  it("keeps a bounded cache through a million rows and preserves nearby values", () => {
    let windows: ResultWindow[] = [];
    for (let start = 0; start < 1_000_000; start += 200) {
      const previous = windows;
      windows = mergeWindow(windows, windowAt(start), start);
      expect(windows.reduce((count, window) => count + window.rows.length, 0)).toBeLessThanOrEqual(resultCacheSize);
      expect(loadedAt(windows, start)).toBe(start + 1);
      expect(firstUncovered(windows, start, start + 199)).toBe(start + 200);
      // Publishing a page must not mutate React's previous snapshot.
      if (previous.length) expect(previous.at(-1)!.rows.at(-1)).toBe(start);
    }
    expect(loadedAt(windows, 0)).toBeUndefined();
    windows = mergeWindow(windows, windowAt(0), 0);
    expect(loadedAt(windows, 0)).toBe(1);
    expect(loadedAt(windows, 999_999)).toBeUndefined();
  });

  it("merges overlaps without duplicates and locates gaps across sparse pages", () => {
    let windows = [windowAt(100), windowAt(500)];
    expect(firstUncovered(windows, 50, 550)).toBe(50);
    expect(firstUncovered(windows, 200, 550)).toBe(300);
    windows = mergeWindow(windows, windowAt(250, 300), 100);
    expect(windows).toEqual([windowAt(100, 600)]);
    expect(firstUncovered(windows, 200, 650)).toBe(651);
  });

  it("uses both nearby cursors and reserves offsets for distant jumps", () => {
    const windows = [windowAt(50_000)];
    const before = planAnchor(String, windows, 49_985);
    expect(before.cursor).toBe("50001");
    expect(before.backwards).toBe(true);
    expect(before.start(200)).toBe(49_800);
    const after = planAnchor(String, windows, 50_210);
    expect(after.cursor).toBe("50200");
    expect(after.backwards).toBe(false);
    expect(after.start(200)).toBe(50_200);
    expect(planAnchor(String, windows, 80_000).offset).toBe(80_000n);
  });
});
