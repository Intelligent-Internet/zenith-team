import { useState } from "react";
import { useLocation } from "react-router";
import { ArrowRight, Building2, Users } from "@/shared/ui/icons";
import { JoinCompanyForm } from "@/features/company-invitations/components/join-company-form";
import { OnboardingScene } from "@/features/onboarding/company/components/onboarding-scene";

type OnboardingIntroductionProps = {
  firstName?: string;
  onCreateCompany: () => void;
};

export function OnboardingIntroduction({
  firstName,
  onCreateCompany,
}: OnboardingIntroductionProps) {
  const location = useLocation();
  const [joiningCompany, setJoiningCompany] = useState(
    location.state?.joinCompany === true,
  );

  return (
    <section
      className="onboarding-introduction"
      aria-labelledby="onboarding-introduction-title"
    >
      <OnboardingScene
        agentId="onboarding-introduction"
        className="onboarding-introduction__scene"
        size={300}
      />
      <div className="onboarding-introduction__copy">
        <p className="onboarding-welcome__eyebrow">
          Your ambition, in good company.
        </p>
        <h1 id="onboarding-introduction-title">
          {joiningCompany ? (
            "Join your team."
          ) : (
            <>
              Hi{firstName ? `, ${firstName}` : " there"}.<br />
              Welcome to Zenith Team.
            </>
          )}
        </h1>
        <p>
          {joiningCompany
            ? "Paste the invite link your team shared with you. You’ll review the company and your role before joining."
            : "Start a company with AI agents, or join the team that’s already waiting for you."}
        </p>
      </div>
      <div className="onboarding-introduction__actions">
        {joiningCompany ? (
          <JoinCompanyForm onBack={() => setJoiningCompany(false)} />
        ) : (
          <div className="onboarding-introduction__choices">
            <button
              autoFocus
              type="button"
              className="onboarding-introduction__choice"
              onClick={onCreateCompany}
            >
              <Building2 size={24} aria-hidden="true" />
              <span>
                <strong>Create a new company</strong>
                <span>Bring your ideas to life with AI agents.</span>
              </span>
              <ArrowRight size={18} aria-hidden="true" />
            </button>
            <button
              type="button"
              className="onboarding-introduction__choice"
              onClick={() => setJoiningCompany(true)}
            >
              <Users size={24} aria-hidden="true" />
              <span>
                <strong>Join an existing company</strong>
                <span>Use an invite link to join your team.</span>
              </span>
              <ArrowRight size={18} aria-hidden="true" />
            </button>
          </div>
        )}
      </div>
    </section>
  );
}
