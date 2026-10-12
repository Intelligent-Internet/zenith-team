import { SocialPostPreview } from "@/features/social/social-post-preview";
import type { ReactNode } from "react";

import {
  formatHitlDate,
  questionStatusLabel,
} from "@/features/approvals/lib/approval-inbox";
import type { QuestionAnswerDto, QuestionDto } from "@/shared/api/types";
import { CheckCircle2, ChevronRight } from "@/shared/ui/icons";
import { QuestionText } from "./question-text";

export function QuestionHistory({
  question,
  compact,
  contextLink,
  pendingAnswers,
}: {
  question: QuestionDto;
  compact: boolean;
  contextLink?: ReactNode;
  pendingAnswers?: QuestionAnswerDto[];
}) {
  const answered = question.status === "answered";
  const sending = pendingAnswers !== undefined;
  const hasAnswers = answered || sending;
  const entries = question.prompts.map((prompt) => {
    const answer = (pendingAnswers ?? question.answers)?.find(
      (entry) => entry.promptId === prompt.id,
    );
    const options =
      prompt.options?.filter((option) =>
        answer?.optionIds?.includes(option.id),
      ) ?? [];
    return { prompt, answer, options };
  });
  const preview = hasAnswers
    ? entries
        .flatMap(({ answer, options }) => [
          ...options.map((option) => option.label),
          answer?.text?.trim(),
        ])
        .filter(Boolean)
        .join("; ") || "No answer submitted"
    : question.title;

  return (
    <section
      aria-busy={sending || undefined}
      aria-label={sending ? "Sending answer" : "Question history"}
      className="agent-question-history"
      id={`question-${question.id}`}
    >
      <details
        className="agent-question-history__disclosure"
        open={compact ? undefined : true}
      >
        <summary
          className="agent-question-history__summary"
          title={question.title}
        >
          {answered ? <CheckCircle2 size={14} aria-hidden /> : null}
          <span className="agent-question-history__status">
            {sending ? "Sending answer…" : questionStatusLabel(question.status)}
          </span>
          <span aria-hidden>·</span>
          <span className="agent-question-history__preview">{preview}</span>
          <ChevronRight
            className="agent-question-history__chevron"
            size={14}
            aria-hidden
          />
        </summary>

        <div className="agent-question-history__details">
          <h3 className="agent-question-history__title">{question.title}</h3>
          <p className="agent-question-history__meta">
            <span>{question.askedByAgent.name} asked</span>
            <span aria-hidden>·</span>
            <time dateTime={question.createdAt}>
              {formatHitlDate(question.createdAt)}
            </time>
          </p>
          <div className="agent-question-history__entries">
            {entries.map(({ prompt, answer, options }, index) => (
              <div className="agent-question-history__entry" key={prompt.id}>
                {entries.length > 1 ? (
                  <p className="agent-question-history__label">
                    Question {index + 1}
                  </p>
                ) : null}
                <QuestionText>{prompt.prompt}</QuestionText>
                {prompt.socialPost ? (
                  <SocialPostPreview
                    reviewed={hasAnswers}
                    post={prompt.socialPost}
                    companyId={question.companyId}
                  />
                ) : null}
                {prompt.helpText ? (
                  <div className="agent-question-history__help">
                    <QuestionText>{prompt.helpText}</QuestionText>
                  </div>
                ) : null}
                {hasAnswers ? (
                  <div className="agent-question-history__answer">
                    <p className="agent-question-history__label">
                      {question.answeredBy
                        ? `${question.answeredBy.name}’s answer`
                        : "Answer"}
                    </p>
                    {options.length ? (
                      <ul className="agent-question-history__choices">
                        {options.map((option) => (
                          <li key={option.id}>{option.label}</li>
                        ))}
                      </ul>
                    ) : null}
                    {answer?.text ? (
                      <p className="agent-question-history__answer-text">
                        {answer.text}
                      </p>
                    ) : null}
                    {!options.length && !answer?.text ? (
                      <p className="agent-question-history__empty">
                        No answer submitted.
                      </p>
                    ) : null}
                  </div>
                ) : null}
              </div>
            ))}
          </div>
          {contextLink ? (
            <footer className="agent-question-history__footer">
              {contextLink}
            </footer>
          ) : null}
        </div>
      </details>
    </section>
  );
}
