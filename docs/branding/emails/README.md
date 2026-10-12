# Zenith Team transactional emails

These HTML sources match the Resend templates updated on 2026-09-17. Edit through
Resend's code editor and publish a new version after previewing. Template IDs and
aliases are intentionally unchanged; server configuration continues to work.

| Source | Resend template ID | Alias | Required variables |
| --- | --- | --- | --- |
| `member-invitation.html` | `b3f71b65-f85b-4fdb-9752-451bce2156c7` | `zenith-team-member-invitation` | `COMPANY_NAME`, `INVITER_NAME`, `INVITATION_URL`, `EXPIRES_AT` |
| `email-verification.html` | `00000000-0000-4000-8000-000000000001` | `boardly-verification` | `DISPLAY_NAME`, `OTP_CODE` |
| `password-reset.html` | `00000000-0000-4000-8000-000000000002` | `password-reset` | `DISPLAY_NAME`, `RESET_URL` |

Sender display name: **Zenith Team**. Existing verified sender address:
`noreply@mail.example.com`. Keep security codes and URLs required, without fake
fallback values. Invitation acceptance finishes in the desktop app.

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
