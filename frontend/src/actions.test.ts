import { describe, expect, it } from "vitest";
import { RenameFileAction } from "./actions";

describe("RenameFileAction", () => {
  it("is offered for exactly one selected item", () => {
    const visibility = (count: number) =>
      RenameFileAction.customVisibility?.({
        selectedFilesForAction: Array.from({ length: count }, (_, index) => ({ id: String(index), name: `file-${index}` })),
      } as Parameters<NonNullable<typeof RenameFileAction.customVisibility>>[0]);
    expect(visibility(0)).toBe(1);
    expect(visibility(1)).toBe(2);
    expect(visibility(2)).toBe(0);
  });
});
