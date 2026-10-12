import { createSlice, type PayloadAction } from "@reduxjs/toolkit";

export type OverviewTaskView = "list" | "gallery" | "table" | "board";
export type OverviewResultView = "list" | "gallery" | "table" | "expanded";
type OverviewViews = { tasks: OverviewTaskView; results: OverviewResultView };

export type ProjectsView = "list" | "grid";

export type WorkView = "board" | "table" | "next_up" | "dependencies";
export type ProjectTaskView = Exclude<WorkView, "next_up">;

export type WorkCompletionReceipt = {
  id: string;
  companyId: string;
  completedTaskIds: string[];
  newlyReadyTaskIds: string[];
  affectedWaitingTaskIds: string[];
  isReconciling: boolean;
  isUnavailable: boolean;
  dismissed: boolean;
};

type WorkState = {
  view: WorkView;
  projectsView?: ProjectsView;
  projectTaskViews?: Record<string, ProjectTaskView>;
  overviewViews?: Record<string, OverviewViews>;
  completionReceipts: WorkCompletionReceipt[];
};

const initialState: WorkState = {
  view: "board",
  projectsView: "list",
  completionReceipts: [],
};

const workSlice = createSlice({
  name: "work",
  initialState,
  reducers: {
    setProjectsView: (state, action: PayloadAction<ProjectsView>) => {
      state.projectsView = action.payload;
    },
    setWorkView: (state, action: PayloadAction<WorkView>) => {
      state.view = action.payload;
    },
    setProjectTaskView: (
      state,
      {
        payload,
      }: PayloadAction<{
        companyId: string;
        projectId: string;
        view: ProjectTaskView;
      }>,
    ) => {
      state.projectTaskViews ??= {};
      state.projectTaskViews[`${payload.companyId}:${payload.projectId}`] =
        payload.view;
    },
    setOverviewViews: (
      state,
      {
        payload,
      }: PayloadAction<{
        companyId: string;
        projectId: string;
        views: OverviewViews;
      }>,
    ) => {
      state.overviewViews ??= {};
      state.overviewViews[`${payload.companyId}:${payload.projectId}`] =
        payload.views;
    },
    upsertWorkCompletion: (
      state,
      action: PayloadAction<Omit<WorkCompletionReceipt, "dismissed">>,
    ) => {
      const existing = state.completionReceipts.find(
        (receipt) => receipt.id === action.payload.id,
      );
      if (existing) {
        Object.assign(existing, action.payload);
        return;
      }
      state.completionReceipts.unshift({
        ...action.payload,
        dismissed: false,
      });
      state.completionReceipts = state.completionReceipts.slice(0, 20);
    },
    dismissWorkCompletion: (state, action: PayloadAction<string>) => {
      const receipt = state.completionReceipts.find(
        (candidate) => candidate.id === action.payload,
      );
      if (receipt) receipt.dismissed = true;
    },
  },
});

export const {
  setProjectsView,
  setWorkView,
  setProjectTaskView,
  setOverviewViews,
  upsertWorkCompletion,
  dismissWorkCompletion,
} = workSlice.actions;
export const workReducer = workSlice.reducer;

export function selectProjectTaskView(
  state: { work: WorkState },
  companyId: string,
  projectId: string,
): ProjectTaskView {
  const view = state.work.projectTaskViews?.[`${companyId}:${projectId}`];
  return view === "table" || view === "dependencies" ? view : "board";
}

export function selectWorkView(state: { work: WorkState }): WorkView {
  const view: unknown = state.work.view;
  return view === "table" || view === "next_up" || view === "dependencies"
    ? view
    : "board";
}

export function selectLatestWorkCompletion(
  state: { work: WorkState },
  companyId: string,
) {
  return state.work.completionReceipts.find(
    (receipt) => receipt.companyId === companyId && !receipt.dismissed,
  );
}

export function selectProjectsView(state: { work: WorkState }): ProjectsView {
  return state.work.projectsView === "grid" ? "grid" : "list";
}

const defaultOverviewViews: OverviewViews = { tasks: "list", results: "list" };
export function selectOverviewViews(
  state: { work: WorkState },
  companyId: string,
  projectId: string,
): OverviewViews {
  return (
    state.work.overviewViews?.[`${companyId}:${projectId}`] ??
    defaultOverviewViews
  );
}
