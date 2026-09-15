import { createContext, useCallback, useContext, useState, type PropsWithChildren, type Dispatch, type SetStateAction } from "react";
import type { Job } from "@/entity";

export type JobListSnapshot = {
  jobs: Job[];
  revision: bigint;
  snapshotRevision: bigint;
  beforeID?: bigint;
  hasMore: boolean;
  scrollTop: number;
};

type Snapshots = Record<string, JobListSnapshot | undefined>;
const JobListState = createContext<[Snapshots, Dispatch<SetStateAction<Snapshots>>] | null>(null);

export const JobListStateProvider = ({ children }: PropsWithChildren) => {
  const state = useState<Snapshots>({});
  return <JobListState.Provider value={state}>{children}</JobListState.Provider>;
};

export const useJobListState = (key = "") => {
  const local = useState<Snapshots>({});
  const [snapshots, setSnapshots] = useContext(JobListState) ?? local;
  const save = useCallback(
    (update: SetStateAction<JobListSnapshot | undefined>) => {
      setSnapshots((current) => ({ ...current, [key]: typeof update === "function" ? update(current[key]) : update }));
    },
    [key, setSnapshots],
  );
  return [snapshots[key], save] as const;
};
