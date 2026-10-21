import type { LegalDocument } from "@/features/public-site/model/legal-document";

export const termsOfService = {
  title: "Terms of Service",
  description:
    "Review the terms that govern access to and use of Zenith Team and its desktop application.",
  lastUpdated: "September 11, 2026",
  sections: [
    {
      id: "acceptance",
      title: "Acceptance of these Terms",
      paragraphs: [
        'These Terms of Service ("Terms") form a binding agreement between you and ii.inc regarding Zenith Team, including the Zenith Team website, hosted services, desktop application, AI features, and related support (collectively, the "Service"). By accessing or using the Service, you agree to these Terms and our Privacy Policy.',
        'If you do not agree, do not access or use the Service. If you use Zenith Team on behalf of a company or other organization, you represent that you have authority to bind that organization, and "you" includes that organization.',
      ],
    },
    {
      id: "eligibility",
      title: "Eligibility and authority",
      paragraphs: [
        "You must be at least 18 years old and legally able to enter into these Terms. You may use the Service only in compliance with applicable law and any rules that apply to your organization, industry, data, or intended use.",
        "Zenith Team is not designed for use by children or for activities that require a person below the age of majority to enter a contract.",
      ],
    },
    {
      id: "accounts",
      title: "Accounts and security",
      paragraphs: [
        "You must provide accurate account information and keep it current. You are responsible for protecting credentials, controlling authorized access, and all activity performed through your account or workspace.",
        "Do not share passwords or access tokens, place credentials in prompts, bypass access controls, or permit unauthorized use. Notify support@ii.inc promptly if you suspect compromise, unauthorized access, or misuse. We may require verification before changing account access or responding to security-sensitive requests.",
      ],
    },
    {
      id: "service",
      title: "The Zenith Team service",
      paragraphs: [
        "Zenith Team provides an AI company workspace that helps founders and operators plan, coordinate, and review work through an AI CEO, specialist Agents, tools, integrations, and generated work products. Features may be experimental, labeled beta, or changed as the Service develops.",
        "We may add, remove, suspend, or change features, providers, limits, supported platforms, or release channels. We do not guarantee that every feature, integration, or desktop build will be available in every country, device, or environment. We may provide updates automatically or require an update to continue using parts of the Service.",
      ],
    },
    {
      id: "license",
      title: "License and restrictions",
      paragraphs: [
        "Subject to these Terms, ii.inc grants you a limited, non-exclusive, non-transferable, non-sublicensable, revocable license to access and use the Service for your internal business purposes. For the desktop application, this license permits installation and use on devices you control or are authorized to use.",
        "You may not copy, sell, lease, sublicense, distribute, or create derivative works from the Service except as law expressly permits. You may not reverse engineer, probe, scan, defeat technical limits, interfere with security or operation, scrape the Service at unreasonable scale, use unauthorized automation, or access the Service to build or train a competing product using protected Zenith Team materials.",
      ],
    },
    {
      id: "acceptable-use",
      title: "Acceptable use",
      paragraphs: [
        "You must not use Zenith Team to:",
        [
          "Break the law, violate another person's rights, or create, distribute, or facilitate illegal, harmful, fraudulent, deceptive, harassing, or infringing material.",
          "Develop malware, evade security controls, steal credentials, gain unauthorized access, disrupt systems, or conceal abusive activity.",
          "Process personal, confidential, or regulated information without the permissions, safeguards, notices, and legal basis required for that use.",
          "Make fully automated high-impact decisions about employment, credit, housing, education, insurance, healthcare, legal rights, or access to essential services without appropriate human review and compliance controls.",
          "Misrepresent AI output as verified fact, professional advice, or a human communication when disclosure is required.",
          "Use the Service in a way that creates unreasonable load, harms other users, or circumvents usage, capability, approval, or safety restrictions.",
        ],
        "We may investigate suspected misuse and take proportionate action, including limiting capabilities, suspending access, preserving relevant records, or reporting conduct when required by law.",
      ],
    },
    {
      id: "your-content",
      title: "Your content",
      paragraphs: [
        'As between you and ii.inc, you retain the rights you have in prompts, company context, files, messages, instructions, feedback, and other materials you submit to the Service ("Your Content"). You are responsible for Your Content and for ensuring you have all rights, permissions, and notices required to use it with Zenith Team.',
        "You grant ii.inc a worldwide, non-exclusive, limited license to host, copy, transmit, process, display, adapt, and otherwise use Your Content only as reasonably necessary to provide, secure, support, and improve the Service, fulfill your instructions, enforce these Terms, and comply with law. This license lasts while needed for those purposes and is subject to our Privacy Policy and retention practices.",
      ],
    },
    {
      id: "ai-output",
      title: "AI features and output",
      paragraphs: [
        "AI systems are probabilistic. Responses and generated work products may be inaccurate, incomplete, outdated, biased, offensive, non-unique, or unsuitable for your purpose. Similar or identical output may be generated for other users.",
        "You are responsible for reviewing output, checking sources and permissions, applying appropriate professional judgment, and deciding whether and how to use it. Zenith Team output is not legal, medical, financial, tax, accounting, employment, compliance, or other professional advice. Do not rely on output as the sole basis for a decision that could materially affect a person's rights, health, safety, livelihood, or access to essential services.",
        "To the extent permitted by law and subject to third-party rights, you may use output generated for you. These Terms do not promise that output is protectable, exclusive, non-infringing, or owned by you in every jurisdiction.",
      ],
    },
    {
      id: "third-party-services",
      title: "Third-party services and integrations",
      paragraphs: [
        "Zenith Team may connect to model providers, authentication providers, productivity tools, storage, communication services, or other third-party products. Third-party services are governed by their own terms and privacy practices, and their availability or behavior is outside our control.",
        "When you enable an integration or instruct Zenith Team to take an external action, you authorize the information exchange and access reasonably necessary to perform that instruction. You are responsible for configuring permissions, reviewing proposed actions, and maintaining any required third-party account or license.",
      ],
    },
    {
      id: "intellectual-property",
      title: "Zenith Team intellectual property",
      paragraphs: [
        "The Service, including its software, design, interfaces, branding, documentation, prompts supplied by Zenith Team, and underlying technology, is owned by ii.inc or its licensors and is protected by intellectual-property and other laws. Except for the limited license in these Terms, no rights are granted to you by implication or otherwise.",
        "Zenith Team, its logos, and related names and marks may not be used in a way that suggests endorsement or affiliation without written permission.",
      ],
    },
    {
      id: "feedback",
      title: "Feedback",
      paragraphs: [
        "If you provide ideas, suggestions, or feedback about Zenith Team, you grant ii.inc a worldwide, perpetual, irrevocable, royalty-free right to use and incorporate that feedback without restriction or compensation. This does not transfer ownership of Your Content submitted for normal Service use.",
      ],
    },
    {
      id: "privacy",
      title: "Privacy",
      paragraphs: [
        "Our Privacy Policy explains how we handle personal information and is incorporated into these Terms. You agree to provide any notices and obtain any permissions required before submitting another person's information to Zenith Team.",
      ],
    },
    {
      id: "termination",
      title: "Suspension, termination, and account deletion",
      paragraphs: [
        "You may stop using Zenith Team at any time and may use available account-deletion controls. You may also contact support@ii.inc for assistance. Deletion and retention are subject to the Privacy Policy, technical backup cycles, security requirements, and applicable law.",
        "We may limit, suspend, or terminate access if we reasonably believe you violated these Terms, created security or legal risk, failed to maintain required third-party access, or used the Service in a way that could harm Zenith Team, users, or others. Where appropriate, we will provide notice and an opportunity to address the issue.",
        "Provisions that by their nature should survive termination will survive, including provisions concerning intellectual property, feedback, disclaimers, liability, indemnity, disputes, and accrued obligations.",
      ],
    },
    {
      id: "disclaimers",
      title: "Disclaimers",
      paragraphs: [
        'TO THE MAXIMUM EXTENT PERMITTED BY LAW, THE SERVICE AND ALL OUTPUT ARE PROVIDED "AS IS" AND "AS AVAILABLE." ii.inc DISCLAIMS ALL EXPRESS, IMPLIED, AND STATUTORY WARRANTIES, INCLUDING WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE, TITLE, NON-INFRINGEMENT, ACCURACY, QUIET ENJOYMENT, AND UNINTERRUPTED OR ERROR-FREE OPERATION.',
        "We do not warrant that the Service will meet every requirement, that output will be correct or unique, that third-party services will remain available, or that all defects, security incidents, or data loss can be prevented. Some jurisdictions do not allow certain warranty exclusions, so part of this section may not apply to you.",
      ],
    },
    {
      id: "liability",
      title: "Limitation of liability",
      paragraphs: [
        "TO THE MAXIMUM EXTENT PERMITTED BY LAW, ii.inc AND ITS AFFILIATES, OFFICERS, EMPLOYEES, AGENTS, LICENSORS, AND SERVICE PROVIDERS WILL NOT BE LIABLE FOR INDIRECT, INCIDENTAL, SPECIAL, CONSEQUENTIAL, EXEMPLARY, OR PUNITIVE DAMAGES; LOSS OF PROFITS, REVENUE, BUSINESS, DATA, GOODWILL, OR OPPORTUNITY; OR THE COST OF SUBSTITUTE SERVICES, ARISING FROM OR RELATED TO ZENITH TEAM, EVEN IF ADVISED OF THE POSSIBILITY.",
        "TO THE MAXIMUM EXTENT PERMITTED BY LAW, THE AGGREGATE LIABILITY OF ii.inc FOR ALL CLAIMS ARISING FROM OR RELATED TO THE SERVICE OR THESE TERMS WILL NOT EXCEED THE GREATER OF US$100 OR THE AMOUNT YOU PAID DIRECTLY TO ii.inc FOR ZENITH TEAM DURING THE 12 MONTHS BEFORE THE EVENT GIVING RISE TO THE CLAIM.",
        "These limits do not exclude liability that cannot lawfully be excluded or limited. Consumer-protection rights that apply to you remain unaffected.",
      ],
    },
    {
      id: "indemnity",
      title: "Indemnity",
      paragraphs: [
        "To the extent permitted by law, you will defend, indemnify, and hold harmless ii.inc and its affiliates, officers, employees, and agents from claims, damages, losses, liabilities, and reasonable costs arising from Your Content, your use of output, your violation of these Terms or applicable law, or your infringement of another person's rights.",
        "We will provide reasonable notice of an indemnified claim and may control its defense and settlement. You may not settle a claim in a way that admits fault or imposes obligations on ii.inc without our written consent.",
      ],
    },
    {
      id: "changes",
      title: "Changes to these Terms",
      paragraphs: [
        "We may update these Terms as Zenith Team, our providers, or applicable law changes. We will post updated Terms with a revised date and provide additional notice when required for a material change. Unless law requires otherwise, updated Terms take effect when posted or on the date stated in the notice.",
        "If you do not agree to updated Terms, you must stop using the Service. Continued use after the effective date means you accept the updated Terms to the extent permitted by law.",
      ],
    },
    {
      id: "governing-law",
      title: "Governing law and disputes",
      paragraphs: [
        "These Terms are governed by the laws of Vietnam, without regard to conflict-of-law principles. Any dispute arising from or relating to these Terms or the Service will be subject to the exclusive jurisdiction of the competent courts in Ho Chi Minh City, Vietnam, unless applicable law requires another forum or gives you non-waivable rights.",
        "Before filing a formal claim, you and ii.inc agree to try in good faith for 30 days to resolve the dispute informally. Send a written description of the dispute and requested resolution to support@ii.inc. This requirement does not prevent either party from seeking urgent injunctive or protective relief.",
      ],
    },
    {
      id: "contact",
      title: "Contact us",
      paragraphs: [
        "Questions about these Terms or the Service may be sent to support@ii.inc. Do not include passwords, access tokens, or unnecessary sensitive information in your message.",
      ],
    },
  ],
} as const satisfies LegalDocument;
