import { apiClient } from "@/lib/api-client";
import { requireActiveCompanyId } from "@/shared/api/active-company";
import type {
  QuestionAnswerRequest,
  QuestionDto,
  QuestionListQuery,
  QuestionPage,
  QuestionWithdrawRequest,
} from "@/shared/api/types";

class QuestionService {
  list(
    query: QuestionListQuery = {},
    companyId = requireActiveCompanyId(),
  ): Promise<QuestionPage> {
    return apiClient.get<QuestionPage>(
      `/companies/${encodeURIComponent(companyId)}/questions`,
      { params: query },
    );
  }

  answer(
    questionId: string,
    request: QuestionAnswerRequest,
    companyId = requireActiveCompanyId(),
  ): Promise<QuestionDto> {
    return apiClient.post<QuestionDto>(
      `/companies/${encodeURIComponent(companyId)}/questions/${encodeURIComponent(questionId)}/answer`,
      request,
    );
  }

  withdraw(
    questionId: string,
    request: QuestionWithdrawRequest,
    companyId = requireActiveCompanyId(),
  ): Promise<QuestionDto> {
    return apiClient.post<QuestionDto>(
      `/companies/${encodeURIComponent(companyId)}/questions/${encodeURIComponent(questionId)}/withdraw`,
      request,
    );
  }
}

export const questionService = new QuestionService();
