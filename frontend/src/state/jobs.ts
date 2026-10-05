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
export const encodeJobList = (value: JobListSnapshot, jobs?: string[]): StoredSnapshot => ({
  ...value,
  jobs: jobs ?? value.jobs.map((job) => Job.toJsonString(job)),
  revision: String(value.revision),
  snapshotRevision: String(value.snapshotRevision),
  beforeID: value.beforeID?.toString(),
});
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
  const selectJobs = createSelector([(state: { jobLists: JobsState }) => state.jobLists[key]?.jobs], (jobs) => jobs?.map((job) => Job.fromJsonString(job)));
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
