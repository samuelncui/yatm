import { createSelector, createSlice, type PayloadAction } from "@reduxjs/toolkit";
import { Job } from "@/entity";

export type JobListSnapshot = {
  jobs: Job[];
  revision: bigint;
  snapshotRevision: bigint;
  beforeID?: bigint;
  hasMore: boolean;
  scrollTop: number;
};
type StoredSnapshot = Omit<JobListSnapshot, "jobs" | "revision" | "snapshotRevision" | "beforeID"> & {
  jobs: string[];
  revision: string;
  snapshotRevision: string;
  beforeID?: string;
};
// Reuse the stored encoding of every Job object carried over from the previous snapshot.
export const encodeJobList = (value: JobListSnapshot, previous?: { jobs: Job[]; encoded: string[] }): StoredSnapshot => {
  const known = new Map(previous?.jobs.map((job, index) => [job, previous.encoded[index]]));
  return {
    ...value,
    jobs: previous && value.jobs === previous.jobs ? previous.encoded : value.jobs.map((job) => known.get(job) ?? Job.toJsonString(job)),
    revision: String(value.revision),
    snapshotRevision: String(value.snapshotRevision),
    beforeID: value.beforeID?.toString(),
  };
};
export type JobsState = Record<string, StoredSnapshot | undefined>;
const jobs = createSlice({
  name: "jobLists",
  initialState: {} as JobsState,
  reducers: {
    saved(state, { payload }: PayloadAction<{ key: string; snapshot: StoredSnapshot }>) {
      state[payload.key] = payload.snapshot;
    },
    scrolled(state, { payload }: PayloadAction<{ key: string; scrollTop: number }>) {
      const snapshot = state[payload.key];
      if (snapshot) snapshot.scrollTop = payload.scrollTop;
    },
  },
});
export const jobListActions = jobs.actions;
export const jobListsReducer = jobs.reducer;
export const makeSelectJobList = (key: string) => {
  const selectStored = (state: { jobLists: JobsState }) => state.jobLists[key];
  // Decode only changed rows, so unchanged Jobs keep their object identity for memoized cards.
  let decoded = new Map<string, Job>();
  const selectJobs = createSelector([(state: { jobLists: JobsState }) => state.jobLists[key]?.jobs], (jobs) => {
    if (!jobs) return undefined;
    const next = new Map<string, Job>();
    const result = jobs.map((json) => {
      const job = decoded.get(json) ?? Job.fromJsonString(json);
      next.set(json, job);
      return job;
    });
    decoded = next;
    return result;
  });
  return createSelector([selectStored, selectJobs], (value, jobs): JobListSnapshot | undefined =>
    value && jobs
      ? {
          ...value,
          jobs,
          revision: BigInt(value.revision),
          snapshotRevision: BigInt(value.snapshotRevision),
          beforeID: value.beforeID === undefined ? undefined : BigInt(value.beforeID),
        }
      : undefined,
  );
};
