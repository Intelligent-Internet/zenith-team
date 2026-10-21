import type { BaseQueryApi } from "@reduxjs/toolkit/query";

import { approvalService, questionService } from "@/shared/api";
import {
  zenithTeamApi,
  executeZenithTeamQuery,
} from "@/shared/api/zenith-team-api";
import type {
  ApprovalCancelRequest,
  ApprovalDto,
  ApprovalListQuery,
  ApprovalPage,
  ApprovalResolveRequest,
  ApprovalStats,
  ApprovalStatsQuery,
  QuestionAnswerRequest,
  QuestionDto,
  QuestionListQuery,
  QuestionPage,
  QuestionWithdrawRequest,
} from "@/shared/api/types";
import type { RootState } from "@/state/store";

type ApprovalListArgument = ApprovalListQuery & {
  allPages?: boolean;
  companyId: string;
};
type ApprovalStatsArgument = ApprovalStatsQuery & { companyId: string };
type ApprovalIdentity = { approvalId: string; companyId: string };
type ApprovalResolutionArgument = ApprovalIdentity & ApprovalResolveRequest;
type ApprovalCancellationArgument = ApprovalIdentity & ApprovalCancelRequest;

type QuestionListArgument = QuestionListQuery & {
  allPages?: boolean;
  companyId: string;
};
type QuestionIdentity = { companyId: string; questionId: string };
type QuestionAnswerArgument = QuestionIdentity & QuestionAnswerRequest;
type QuestionWithdrawalArgument = QuestionIdentity & QuestionWithdrawRequest;

const approvalItemTag = (companyId: string, approvalId: string) =>
  `${companyId}:item:${approvalId}`;
// The rollup is counted server-side, so a realtime frame carrying a whole
// approval row still cannot converge it — it needs a tag of its own to be
// re-read without dragging the list refetch back with it.
const approvalStatsTag = (companyId: string) => `${companyId}:stats`;
const questionItemTag = (companyId: string, questionId: string) =>
  `${companyId}:item:${questionId}`;

async function collectHumanInputPages<T extends ApprovalDto | QuestionDto>(
  readPage: (cursor?: string) => Promise<{
    items: T[];
    nextCursor: string | null;
  }>,
  cursor?: string,
) {
  const items = new Map<string, T>();
  const visitedCursors = new Set<string>();

  while (true) {
    if (cursor) {
      if (visitedCursors.has(cursor)) {
        throw new Error("Pending requests could not be fully loaded.");
      }
      visitedCursors.add(cursor);
    }
    const page = await readPage(cursor);
    for (const item of page.items) {
      const current = items.get(item.id);
      if (!current || current.revision <= item.revision) {
        items.set(item.id, item);
      }
    }
    if (page.nextCursor === null) {
      return { items: [...items.values()], nextCursor: null };
    }
    cursor = page.nextCursor;
  }
}

export const approvalsApi = zenithTeamApi.injectEndpoints({
  endpoints: (builder) => ({
    listApprovals: builder.query<ApprovalPage, ApprovalListArgument>({
      providesTags: (result, _error, { companyId }) => [
        { id: companyId, type: "Approval" },
        ...(result?.items.map((approval) => ({
          id: approvalItemTag(companyId, approval.id),
          type: "Approval" as const,
        })) ?? []),
      ],
      queryFn: ({ allPages, companyId, ...query }) =>
        executeZenithTeamQuery(
          () =>
            allPages
              ? collectHumanInputPages(
                  (cursor) =>
                    approvalService.list(
                      { ...query, cursor, limit: query.limit ?? 100 },
                      companyId,
                    ),
                  query.cursor,
                )
              : approvalService.list(query, companyId),
          "Approvals could not be loaded.",
        ),
    }),
    getApprovalStats: builder.query<ApprovalStats, ApprovalStatsArgument>({
      providesTags: (_result, _error, { companyId }) => [
        { id: companyId, type: "Approval" },
        { id: approvalStatsTag(companyId), type: "Approval" },
      ],
      queryFn: ({ companyId, ...query }) =>
        executeZenithTeamQuery(
          () => approvalService.stats(query, companyId),
          "Approval statistics could not be loaded.",
        ),
    }),
    getApproval: builder.query<ApprovalDto, ApprovalIdentity>({
      providesTags: (_result, _error, { approvalId, companyId }) => [
        { id: approvalItemTag(companyId, approvalId), type: "Approval" },
      ],
      queryFn: ({ approvalId, companyId }) =>
        executeZenithTeamQuery(
          () => approvalService.get(approvalId, companyId),
          "Approval could not be loaded.",
        ),
    }),
    approveApproval: builder.mutation<ApprovalDto, ApprovalResolutionArgument>({
      invalidatesTags: (_result, _error, { approvalId, companyId }) => [
        { id: companyId, type: "Approval" },
        { id: approvalItemTag(companyId, approvalId), type: "Approval" },
      ],
      queryFn: ({ approvalId, companyId, ...request }, api) =>
        executeZenithTeamQuery(async () => {
          const approval = await approvalService.approve(
            approvalId,
            request,
            companyId,
          );
          cacheResolvedApproval(api, approval);
          return approval;
        }, "Approval could not be approved."),
    }),
    rejectApproval: builder.mutation<ApprovalDto, ApprovalResolutionArgument>({
      invalidatesTags: (_result, _error, { approvalId, companyId }) => [
        { id: companyId, type: "Approval" },
        { id: approvalItemTag(companyId, approvalId), type: "Approval" },
      ],
      queryFn: ({ approvalId, companyId, ...request }, api) =>
        executeZenithTeamQuery(async () => {
          const approval = await approvalService.reject(
            approvalId,
            request,
            companyId,
          );
          cacheResolvedApproval(api, approval);
          return approval;
        }, "Approval could not be rejected."),
    }),
    cancelApproval: builder.mutation<ApprovalDto, ApprovalCancellationArgument>(
      {
        invalidatesTags: (_result, _error, { approvalId, companyId }) => [
          { id: companyId, type: "Approval" },
          { id: approvalItemTag(companyId, approvalId), type: "Approval" },
        ],
        queryFn: ({ approvalId, companyId, ...request }, api) =>
          executeZenithTeamQuery(async () => {
            const approval = await approvalService.cancel(
              approvalId,
              request,
              companyId,
            );
            cacheResolvedApproval(api, approval);
            return approval;
          }, "Approval could not be cancelled."),
      },
    ),
    listQuestions: builder.query<QuestionPage, QuestionListArgument>({
      providesTags: (result, _error, { companyId }) => [
        { id: companyId, type: "Question" },
        ...(result?.items.map((question) => ({
          id: questionItemTag(companyId, question.id),
          type: "Question" as const,
        })) ?? []),
      ],
      queryFn: ({ allPages, companyId, ...query }) =>
        executeZenithTeamQuery(
          () =>
            allPages
              ? collectHumanInputPages(
                  (cursor) =>
                    questionService.list(
                      { ...query, cursor, limit: query.limit ?? 100 },
                      companyId,
                    ),
                  query.cursor,
                )
              : questionService.list(query, companyId),
          "Questions could not be loaded.",
        ),
    }),
    answerQuestion: builder.mutation<QuestionDto, QuestionAnswerArgument>({
      invalidatesTags: (_result, _error, { companyId, questionId }) => [
        { id: companyId, type: "Question" },
        { id: questionItemTag(companyId, questionId), type: "Question" },
      ],
      async onQueryStarted(_argument, { dispatch, getState, queryFulfilled }) {
        try {
          const { data } = await queryFulfilled;
          for (const argument of approvalsApi.util.selectCachedArgsForQuery(
            getState(),
            "listQuestions",
          )) {
            if (argument.companyId !== data.companyId) continue;
            dispatch(
              approvalsApi.util.updateQueryData(
                "listQuestions",
                argument,
                (page) => {
                  const index = page.items.findIndex(
                    (item) => item.id === data.id,
                  );
                  if (index < 0 || page.items[index].revision > data.revision)
                    return;
                  if (
                    argument.status?.length &&
                    !argument.status.includes(data.status)
                  ) {
                    page.items.splice(index, 1);
                  } else {
                    page.items[index] = data;
                  }
                },
              ),
            );
          }
        } catch {
          // The card keeps its draft and reports mutation errors for retry.
        }
      },
      queryFn: ({ companyId, questionId, ...request }) =>
        executeZenithTeamQuery(
          () => questionService.answer(questionId, request, companyId),
          "Question could not be answered.",
        ),
    }),
    withdrawQuestion: builder.mutation<QuestionDto, QuestionWithdrawalArgument>(
      {
        invalidatesTags: (_result, _error, { companyId, questionId }) => [
          { id: companyId, type: "Question" },
          { id: questionItemTag(companyId, questionId), type: "Question" },
        ],
        queryFn: ({ companyId, questionId, ...request }) =>
          executeZenithTeamQuery(
            () => questionService.withdraw(questionId, request, companyId),
            "Question could not be withdrawn.",
          ),
      },
    ),
  }),
  overrideExisting: false,
});

// Show the committed decision without waiting for realtime delivery or delayed
// tag invalidation. A newer continuation frame must win over the HTTP response.
function cacheResolvedApproval(
  { dispatch, getState }: Pick<BaseQueryApi, "dispatch" | "getState">,
  approval: ApprovalDto,
): void {
  for (const argument of approvalsApi.util.selectCachedArgsForQuery(
    getState() as RootState,
    "listApprovals",
  )) {
    if (argument.companyId !== approval.companyId) continue;
    dispatch(
      approvalsApi.util.updateQueryData("listApprovals", argument, (page) => {
        const index = page.items.findIndex((item) => item.id === approval.id);
        if (index < 0 || page.items[index].revision > approval.revision) return;
        if (
          argument.status?.length &&
          !argument.status.includes(approval.status)
        ) {
          page.items.splice(index, 1);
        } else {
          page.items[index] = approval;
        }
      }),
    );
  }
  dispatch(
    approvalsApi.util.updateQueryData(
      "getApproval",
      { approvalId: approval.id, companyId: approval.companyId },
      (current) => {
        if (current.revision <= approval.revision) {
          Object.assign(current, approval);
        }
      },
    ),
  );
}

export const {
  useAnswerQuestionMutation,
  useApproveApprovalMutation,
  useCancelApprovalMutation,
  useGetApprovalQuery,
  useGetApprovalStatsQuery,
  useLazyListApprovalsQuery,
  useLazyListQuestionsQuery,
  useListApprovalsQuery,
  useListQuestionsQuery,
  useRejectApprovalMutation,
  useWithdrawQuestionMutation,
} = approvalsApi;
