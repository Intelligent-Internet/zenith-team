import {
  normalizeEmail,
  validateEmail,
} from "@/features/auth/model/validation";

export function validateInvitationEmails(
  input: string,
  currentUserEmail: string,
  memberEmails: readonly string[],
) {
  const emails = input
    .split(/[,\r\n]+/)
    .map(normalizeEmail)
    .filter(Boolean);
  const members = new Set(memberEmails.map(normalizeEmail));
  const seen = new Set<string>();
  const errors: string[] = [];

  for (const email of emails) {
    if (validateEmail(email))
      errors.push(`${email}: enter a valid email address.`);
    else if (seen.has(email))
      errors.push(`${email}: this email is entered more than once.`);
    else if (email === normalizeEmail(currentUserEmail))
      errors.push(`${email}: you’re already in this company.`);
    else if (members.has(email))
      errors.push(`${email}: already a company member.`);
    seen.add(email);
  }

  return { emails, errors: [...new Set(errors)] };
}
