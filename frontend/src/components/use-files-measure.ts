import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FileArray } from "@samuelncui/chonky";
import { filesCli } from "@/api";
import { type FileOperationRef, type FilesMeasurement, type MeasureFilesRequest } from "@/entity";
import { errorMessage } from "@/tools";

export function measurementKey(reference?: FileOperationRef): string {
  const target = reference?.target;
  if (target?.oneofKind === "fileId") return `library:${target.fileId}`;
  if (target?.oneofKind === "location") return `location:${target.location.locationId}:${target.location.path}`;
  return "";
}

export type MeasurementState = { running: boolean; summary?: FilesMeasurement; error?: string };

/** Explicit measurement has its own cancellable lifetime, independent of list pagination. */
export function useFilesMeasure(files: FileArray) {
  const [state, setState] = useState<MeasurementState>({ running: false });
  const [sizes, setSizes] = useState<Record<string, FilesMeasurement>>({});
  const active = useRef<AbortController | undefined>(undefined);
  const visible = useRef(new Set<string>());
  useEffect(() => {
    visible.current = new Set(files.map((file) => measurementKey(file?.operationReference as FileOperationRef | undefined)).filter(Boolean));
  }, [files]);

  const invalidate = useCallback(() => {
    active.current?.abort();
    active.current = undefined;
    setState({ running: false });
    setSizes({});
  }, []);

  const cancel = useCallback(() => {
    active.current?.abort();
    active.current = undefined;
    setState({ running: false, error: "Calculation canceled" });
    setSizes({});
  }, []);

  const start = useCallback(async (request: MeasureFilesRequest) => {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setSizes({});
    setState({ running: true });
    try {
      let summary: FilesMeasurement | undefined;
      const call = filesCli.measure(request, { abort: controller.signal });
      for await (const update of call.responses) {
        if (active.current !== controller || controller.signal.aborted) return;
        if (update.result.oneofKind === "summary") {
          summary = update.result.summary;
        } else if (update.result.oneofKind === "item") {
          const item = update.result.item;
          const key = measurementKey(item.reference);
          if (visible.current.has(key)) setSizes((current) => ({ ...current, [key]: item }));
        }
      }
      if (active.current !== controller || controller.signal.aborted) return;
      if (!summary) throw new Error("Calculation ended before its final result");
      setState({ running: false, summary });
    } catch (error) {
      if (active.current !== controller || controller.signal.aborted) return;
      setState({ running: false, error: errorMessage(error, "Could not calculate data usage") });
    } finally {
      if (active.current === controller) active.current = undefined;
    }
  }, []);

  useEffect(() => () => active.current?.abort(), []);
  const measuredFiles = useMemo(
    () =>
      files.map((file) => {
        if (!file) return file;
        const result = sizes[measurementKey(file.operationReference as FileOperationRef | undefined)];
        return result?.complete ? { ...file, size: Number(result.knownBytes) } : file;
      }),
    [files, sizes],
  );
  return { state, files: measuredFiles, start, cancel, invalidate };
}
