# Zenith Team transactional emails

Zenith Team sends sign-up codes, password resets, and member invitations
through [Resend](https://resend.com) templates. The templates live in your own
Resend account; these files are their HTML sources.

To turn email on:

1. In Resend, verify a sending domain.
2. Create one template per file below: paste the HTML, add the listed
   variables, and publish it.
3. Put each template's ID in the matching server setting.

| Source | Server setting | Required variables |
| --- | --- | --- |
| `email-verification.html` | `AUTH_EMAIL_VERIFICATION_TEMPLATE_ID` (required) | `DISPLAY_NAME`, `OTP_CODE` |
| `password-reset.html` | `AUTH_PASSWORD_RESET_TEMPLATE_ID` (required) | `DISPLAY_NAME`, `RESET_URL` |
| `member-invitation.html` | `AUTH_INVITATION_TEMPLATE_ID` (optional; turns invitation emails on) | `COMPANY_NAME`, `INVITER_NAME`, `INVITATION_URL`, `EXPIRES_AT` |

Use **Zenith Team** as the sender display name. Keep security codes and URLs
required, without fake fallback values. Invitation acceptance finishes in the
desktop app.

The visual foundation comes from `frontend/src/features/public-site/styles/studio-page.css`:
sky `#BCE8FA`, cream `#FFFDF7`, navy `#173E5C`, butter `#FFF0B9`. Desktop's
**Zenith Mist** preset softens that foundation for prolonged reading: landing-button navy
`#183F5E` navigation with cream `#FFFDF7` labels and icons, mist `#E7ECEB` sidebar, cream-gray `#F3F2ED` content,
charcoal `#303B40` text and navy `#244B61` actions. Its stored `zenith-sky` ID
is retained so existing selections receive the refinement. New installations default to light
mode; explicit saved themes and color modes remain unchanged. Authentication
screens use the default brand palette, including invitation and verification.

Email layouts use presentation tables, inline colors, system sans-serif text and
Georgia headings, with mobile spacing overrides. They do not depend on remote
images or web fonts. Actual inbox rendering can vary by email client.
