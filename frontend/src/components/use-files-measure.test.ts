import { act, renderHook, waitFor } from "@testing-library/react";
import type { FileArray } from "@samuelncui/chonky";
import { expect, it, vi } from "vitest";
import { FilesMeasurement, MeasureFilesRequest } from "@/entity";
import { useFilesMeasure } from "./use-files-measure";

const { measure } = vi.hoisted(() => ({ measure: vi.fn() }));
vi.mock("@/api", () => ({ filesCli: { measure } }));

it("applies a measured size to a row loaded after the measurement streamed it", async () => {
  const reference = { target: { oneofKind: "fileId" as const, fileId: 7n } };
  measure.mockReturnValue({
    responses: (async function* () {
      yield { result: { oneofKind: "item", item: FilesMeasurement.create({ reference, knownBytes: 42n, complete: true }) } };
      yield { result: { oneofKind: "summary", summary: FilesMeasurement.create({ knownBytes: 42n, complete: true }) } };
    })(),
  });
  const { result, rerender } = renderHook(({ files }: { files: FileArray }) => useFilesMeasure(files), { initialProps: { files: [] as FileArray } });
  await act(() => result.current.start(MeasureFilesRequest.create()));
  await waitFor(() => expect(result.current.state.running).toBe(false));

  // The row arrives on a later page; pagination does not start another measurement.
  rerender({ files: [{ id: "7", name: "folder", isDir: true, operationReference: reference }] as FileArray });
  expect(result.current.files[0]?.size).toBe(42);
  expect(measure).toHaveBeenCalledOnce();
});
