import type { components as ApiComponents } from "@/shared/api/generated/api.generated";

export type MailboxStatus = ApiComponents["schemas"]["MailboxStatus"];

export type CompanyMailbox = ApiComponents["schemas"]["CompanyMailbox"];

export type MailboxState = ApiComponents["schemas"]["MailboxState"];

export type MailboxMessage = ApiComponents["schemas"]["MailboxMessage"];

export type MailboxMessagePage = ApiComponents["schemas"]["MailboxMessagePage"];

export type MailboxAttachment = ApiComponents["schemas"]["MailboxAttachment"];

export type MailboxMessageDetail =
  ApiComponents["schemas"]["MailboxMessageDetail"];

export type MailboxAttachmentDownload =
  ApiComponents["schemas"]["MailboxAttachmentDownload"];
