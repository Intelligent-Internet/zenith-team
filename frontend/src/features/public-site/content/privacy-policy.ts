import type { LegalDocument } from "@/features/public-site/model/legal-document";

export const privacyPolicy = {
  title: "Privacy Policy",
  description:
    "Learn how Zenith Team collects, uses, shares, protects, and retains personal information.",
  lastUpdated: "September 11, 2026",
  sections: [
    {
      id: "scope",
      title: "Scope",
      paragraphs: [
        'This Privacy Policy explains how Zenith Team, a product of ii.inc ("Zenith Team," "we," "us," or "our"), handles personal information when you visit our public website, create or use a Zenith Team account, use the Zenith Team desktop application, contact support, or otherwise interact with our services.',
        "This policy applies to Zenith Team services that link to it. It does not govern third-party products, websites, or integrations that have their own privacy notices. If you use Zenith Team for an organization, that organization may also control information submitted through its workspace and may have its own privacy obligations.",
      ],
    },
    {
      id: "information-you-provide",
      title: "Information you provide",
      paragraphs: [
        "We collect information that you or your organization choose to provide, including:",
        [
          "Account and profile details, such as your name, email address, role, authentication method, timezone, and workspace information.",
          "Company context and workspace content, including goals, business context, prompts, messages, decisions, uploaded materials, generated work products, and other information you submit to Zenith Team.",
          "Integration information and instructions when you choose to connect a third-party service. We do not ask you to place passwords or private access tokens inside prompts.",
          "Support communications, feedback, survey responses, and any files or diagnostic details you intentionally send to us.",
        ],
        "You are responsible for ensuring that you have the rights and permissions needed to submit personal information or confidential material to Zenith Team.",
      ],
    },
    {
      id: "information-collected",
      title: "Information collected when you use Zenith Team",
      paragraphs: [
        "When you use Zenith Team, we may collect limited operational information needed to provide, secure, and maintain the service, including:",
        [
          "Authentication and session records, such as login time, session state, account identifiers, and security events.",
          "Device and browser information, such as device type, operating system, application version, browser type, language, approximate network location derived from an IP address, and request timestamps.",
          "Service activity and reliability information, such as feature interactions, agent workflow status, errors, security records, and performance logs.",
          "Desktop crash metadata when crash reporting is enabled for a release, limited to technical fields such as app version, platform, architecture, release channel, environment, and configured crash-report provider.",
          "Support diagnostics that you explicitly start in the desktop app. These diagnostics may include short-lived network and application traces saved on your device for support review.",
        ],
        "We design diagnostic collection to avoid account credentials, prompts, company content, and secrets, but technical logs can still contain information about requests or device activity. Review diagnostic files before sharing them with support.",
      ],
    },
    {
      id: "how-we-use-information",
      title: "How we use information",
      paragraphs: [
        "We use information to:",
        [
          "Provide, operate, maintain, and improve Zenith Team and its desktop application.",
          "Create and secure accounts, authenticate sessions, and complete onboarding.",
          "Process prompts, coordinate requested AI workflows, preserve company context, and deliver work products.",
          "Provide support, diagnose failures, recover service, and communicate about security or material service changes.",
          "Monitor misuse, enforce our Terms, protect users and systems, and meet legal obligations.",
          "Understand service reliability and make product decisions using information that is aggregated or otherwise handled in accordance with this policy.",
        ],
        "Where applicable law requires a legal basis, we process information as necessary to perform our contract with you, pursue legitimate interests such as service security and improvement, comply with legal obligations, or act with your consent. The available basis depends on the context and jurisdiction.",
      ],
    },
    {
      id: "ai-processing",
      title: "AI processing",
      paragraphs: [
        "Zenith Team uses artificial intelligence systems to interpret requests, coordinate company workflows, and generate responses or work products. To perform a request, relevant prompts, messages, company context, files, and instructions may be transmitted to model, infrastructure, and tool providers acting for us or at your direction.",
        "Do not submit sensitive information that is unnecessary for the requested task, and do not submit information you are not authorized to use. AI output can be inaccurate, incomplete, or unsuitable for your situation. You are responsible for reviewing output before relying on it or using it in decisions, communications, or external systems.",
      ],
    },
    {
      id: "cookies-and-local-storage",
      title: "Cookies and local storage",
      paragraphs: [
        "Zenith Team uses essential browser storage and authentication technologies to keep you signed in, protect sessions, remember onboarding state, and support security. These technologies may include secure authentication cookies, local storage, and session storage.",
        "This release of the public site does not load optional marketing analytics or advertising tags. If we introduce optional analytics later, we will update this policy and provide consent controls where required before activating that collection.",
        "Blocking essential storage may prevent login, onboarding, account security, or other core service functions from working correctly.",
      ],
    },
    {
      id: "sharing",
      title: "How we share information",
      paragraphs: [
        "We may share information only as reasonably necessary for these purposes:",
        [
          "With hosting, infrastructure, email, security, support, AI model, and other service providers that process information for us under contractual restrictions.",
          "With Google when you choose Google sign-in, subject to Google's terms and privacy practices.",
          "With third-party integrations or external services that you or an authorized workspace user choose to enable or instruct Zenith Team to use.",
          "With professional advisers, regulators, courts, law enforcement, or other parties when we reasonably believe disclosure is required by law or necessary to protect rights, safety, users, or the service.",
          "In connection with a merger, financing, acquisition, reorganization, sale of assets, or similar business transaction, subject to appropriate confidentiality and notice where required.",
        ],
        "We do not disclose company workspace content to third parties for their independent use except at your direction, as described in this policy, or as required by law.",
      ],
    },
    {
      id: "commerce-and-payments",
      title: "Commerce, payments, and Stripe Connect",
      paragraphs: [
        "When an authorized Company owner enables Zenith Team Commerce, Stripe collects the legal-entity, representative, owner, identity-verification, tax, and bank-account information required to create and operate that Company's Stripe Connected Account. Stripe collects sensitive identity documents and bank-account details directly under its own agreements and privacy policy; Zenith Team does not ask you to enter those values into prompts, Tasks, or workspace resources.",
        "Zenith Team receives and stores a limited provider projection needed to authorize and operate Commerce, such as the Connected Account identifier, country, capability and verification status, bounded requirement counts, payout readiness, safe balance summaries, payment-operation status, and reconciliation timestamps. Zenith Team may exchange customer, order, payment, subscription, refund, dispute, and payout information with Stripe when necessary to perform an approved Commerce action, prevent fraud, support customers, keep records, and comply with law.",
      ],
    },
    {
      id: "international-transfers",
      title: "International data transfers",
      paragraphs: [
        "Zenith Team and its service providers may process information in countries other than the country where you live. Those countries may have different data-protection laws.",
        "Where required, we use appropriate safeguards for international transfers, such as contractual protections, approved transfer mechanisms, or other measures recognized by applicable law. You may contact us for more information about safeguards relevant to your information.",
      ],
    },
    {
      id: "retention",
      title: "Data retention",
      paragraphs: [
        "We retain information for as long as reasonably necessary to provide Zenith Team, maintain account and company history, meet the purposes described in this policy, resolve disputes, enforce agreements, and comply with legal obligations.",
        "Retention periods depend on the type of information, why it was collected, workspace instructions, security needs, and applicable law. When an account or workspace is deleted, some information may remain for a limited period in backups or may be retained when necessary for security, fraud prevention, audit integrity, dispute resolution, or legal compliance. We delete or de-identify information when it is no longer reasonably needed.",
      ],
    },
    {
      id: "security",
      title: "Security",
      paragraphs: [
        "We use administrative, technical, and organizational safeguards designed to protect information, including access controls, scoped authorization, secure authentication, logging safeguards, and efforts to redact sensitive diagnostic data.",
        "No service, network, or storage system is completely secure. You are responsible for protecting your credentials, limiting sensitive content to what a task requires, and notifying us promptly if you believe your account or workspace has been compromised.",
      ],
    },
    {
      id: "your-rights",
      title: "Your choices and privacy rights",
      paragraphs: [
        "Depending on where you live, you may have rights to request access, correction, deletion, portability, restriction, or objection; withdraw consent where processing relies on consent; or complain to a data-protection authority.",
        "You may update certain account and company information in Zenith Team and may use available account-deletion controls. To make another privacy request, email support@ii.inc. We may need to verify your identity and authority before completing a request. Rights and exceptions vary by jurisdiction, and we may retain information where law permits or requires it.",
        "If your account is managed by an organization, the organization may be the appropriate contact for requests about workspace content. We may refer the request to that organization when appropriate.",
      ],
    },
    {
      id: "children",
      title: "Children's privacy",
      paragraphs: [
        "Zenith Team is a business service and is not directed to children under 16. We do not knowingly collect personal information from children under 16. If you believe a child has provided personal information to Zenith Team, contact support@ii.inc so we can review the situation and take appropriate action.",
      ],
    },
    {
      id: "changes",
      title: "Changes to this policy",
      paragraphs: [
        "We may update this Privacy Policy to reflect changes in Zenith Team, our providers, or applicable law. We will post the updated policy with a revised date and provide additional notice when a material change requires it. Your continued use after an update is subject to the updated policy to the extent permitted by law.",
      ],
    },
    {
      id: "contact",
      title: "Contact us",
      paragraphs: [
        "Zenith Team is a product of ii.inc. For privacy questions, requests, or complaints, contact support@ii.inc. Please describe the request and the Zenith Team account or workspace involved without sending passwords, access tokens, or other secrets.",
      ],
    },
  ],
} as const satisfies LegalDocument;
