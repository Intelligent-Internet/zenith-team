import { useEffect, useRef, useState, type FormEvent } from "react";
import { ChevronDown, LogOut } from "@/shared/ui/icons";
import { Link, useNavigate } from "react-router";

import {
  readSessionSnapshot,
  updateSessionUserSummary,
  type SessionSnapshot,
} from "@/shared/session/session";
import {
  deleteZenithTeamAccount,
  logoutZenithTeamSession,
} from "@/features/auth/model/logout";
import { WorkspaceCompanySwitcher } from "@/app/shell/workspace-company-switcher";
import { SettingsButton } from "@/features/settings/components/settings-primitives";
import { AvatarImagePicker } from "@/features/settings/components/avatar-image-picker";
import { createProfileAvatarDataUrl } from "@/features/settings/model/profile-avatar";
import "@/features/settings/styles/settings.css";
import "@/features/settings/styles/my-account.css";
import { useActivityInsight } from "@/features/settings/hooks/use-activity-insight";
import { accountApi, useGetCurrentUserQuery } from "@/shared/api/account-api";
import { COUNTRY_CALLING_CODES } from "@/features/settings/model/country-calling-codes";
import {
  getApiErrorBody,
  getApiErrorCode,
  getApiErrorMessage,
} from "@/lib/api-client";
import { authService } from "@/shared/api/auth.service";
import type { AuthUserResponse } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import { publicAssetPath } from "@/shared/lib/public-assets";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";
import { ProfileDailyBriefSection } from "@/features/scheduled/components/profile-daily-brief-section";
import { useCan } from "@/shared/permissions";
import { useAppDispatch } from "@/state";

type ProfileFormValues = {
  biography: string;
  countryIso2: string;
  displayName: string;
  phoneNumber: string;
};

const emptyProfileFormValues: ProfileFormValues = {
  biography: "",
  countryIso2: "VN",
  displayName: "",
  phoneNumber: "",
};

function createProfileFormValues(user: AuthUserResponse): ProfileFormValues {
  return {
    biography: user.biography,
    countryIso2: user.phoneCountryIso2 ?? "VN",
    displayName: user.displayName,
    phoneNumber: user.phoneNumber ?? "",
  };
}

function profileConflictUser(error: unknown): AuthUserResponse | null {
  if (getApiErrorCode(error) !== "profile_revision_conflict") {
    return null;
  }
  return (
    (getApiErrorBody(error)?.currentUser as AuthUserResponse | undefined) ??
    null
  );
}

type ActivityMetricProps = {
  label: string;
  loading: boolean;
  primary: string;
  status?: string;
};

function ActivityMetric({
  label,
  loading,
  primary,
  status,
}: ActivityMetricProps) {
  return (
    <div className="my-account-metric min-w-0">
      <dt>{label}</dt>
      <dd>
        {loading ? (
          <span
            aria-label={`Loading ${label}`}
            className="block w-[70%] h-[24px] mt-[4px] rounded-[4px] [background:var(--workspace-control-bg)]"
          />
        ) : (
          primary
        )}
        {!loading && status ? <small>{status}</small> : null}
      </dd>
    </div>
  );
}

function formatActivityCount(value: string) {
  try {
    return new Intl.NumberFormat().format(BigInt(value));
  } catch {
    return value;
  }
}

function formatModelIdentifier(value: string) {
  return value
    .split(/[-_]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1).toLowerCase())
    .join(" ");
}

function formatActivityModel(
  provider: string | null | undefined,
  model: string | null | undefined,
) {
  if (!provider || !model) return null;
  const parts = model.split(/[-_]+/).filter(Boolean);
  const family = parts[0]?.toLowerCase();
  if (family === "gpt" || family === "claude") {
    return [
      family === "gpt" ? "GPT" : "Claude",
      ...parts.slice(1).map(formatModelIdentifier),
    ].join(" ");
  }
  return `${formatModelIdentifier(provider)} ${model}`;
}

export function ProfileSettingsPage() {
  const dispatch = useAppDispatch();
  const navigate = useNavigate();
  const profileQuery = useGetCurrentUserQuery();
  const activityInsight = useActivityInsight();
  const activityModel = formatActivityModel(
    activityInsight.data?.mostUsedProvider,
    activityInsight.data?.mostUsedModel,
  );
  const [session, setSession] = useState<SessionSnapshot>(() => {
    return readSessionSnapshot();
  });
  const canManageSchedules = useCan("schedules.manage");
  const [profileUser, setProfileUser] = useState<AuthUserResponse | null>(null);
  const [initialValues, setInitialValues] = useState<ProfileFormValues>(
    emptyProfileFormValues,
  );
  const [formValues, setFormValues] = useState<ProfileFormValues>(
    emptyProfileFormValues,
  );
  const [isSavingProfile, setIsSavingProfile] = useState(false);
  const [avatarFile, setAvatarFile] = useState<File | null>(null);
  const [profileFeedback, setProfileFeedback] = useState<string | null>(null);
  const [profileError, setProfileError] = useState<string | null>(null);
  const [conflictUser, setConflictUser] = useState<AuthUserResponse | null>(
    null,
  );
  const [isSigningOut, setIsSigningOut] = useState(false);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [deleteConfirmation, setDeleteConfirmation] = useState("");
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [isDeleting, setIsDeleting] = useState(false);
  const authoritativeRevisionRef = useRef(0);
  if (
    profileQuery.currentData &&
    profileQuery.currentData.revision > authoritativeRevisionRef.current
  ) {
    authoritativeRevisionRef.current = profileQuery.currentData.revision;
  }

  useEffect(() => {
    if (!profileQuery.currentData || profileUser) return;
    const user = profileQuery.currentData;
    const nextValues = createProfileFormValues(user);
    authoritativeRevisionRef.current = user.revision;
    setProfileUser(user);
    setInitialValues(nextValues);
    setFormValues(nextValues);
    setSession(updateSessionUserSummary(user));
    setProfileError(null);
  }, [profileQuery.currentData, profileUser]);

  useEffect(() => {
    if (!profileUser && profileQuery.error) {
      setProfileError(
        profileQuery.error.message ??
          "Zenith Team could not load your profile.",
      );
    }
  }, [profileQuery.error, profileUser]);

  const isLoadingProfile = profileQuery.isLoading && !profileUser;

  const isDisplayNameDirty =
    formValues.displayName !== initialValues.displayName;
  const isBiographyDirty = formValues.biography !== initialValues.biography;
  const isPhoneDirty =
    formValues.phoneNumber !== initialValues.phoneNumber ||
    (formValues.phoneNumber !== "" &&
      formValues.countryIso2 !== initialValues.countryIso2);
  const isProfileDirty =
    avatarFile !== null ||
    isDisplayNameDirty ||
    isBiographyDirty ||
    isPhoneDirty;
  const profileFieldsDisabled =
    isLoadingProfile || isSavingProfile || !profileUser;
  const biographyLineCount = formValues.biography
    ? formValues.biography.split("\n").length
    : 0;
  const avatarUrl =
    session.avatarUrl?.trim() ||
    publicAssetPath("images/settings/profile-avatar.svg");

  const updateField = <Field extends keyof ProfileFormValues>(
    field: Field,
    value: ProfileFormValues[Field],
  ) => {
    setFormValues((current) => ({ ...current, [field]: value }));
    setProfileFeedback(null);
    setProfileError(null);
  };

  const handleBiographyChange = (value: string) => {
    const normalized = value.replace(/\r\n?/g, "\n");
    const boundedLines = normalized.split("\n").slice(0, 12).join("\n");
    updateField("biography", Array.from(boundedLines).slice(0, 1200).join(""));
  };

  const handlePhoneChange = (value: string) => {
    updateField("phoneNumber", value.replace(/\D/g, "").slice(0, 15));
  };

  const applyAuthoritativeProfile = (user: AuthUserResponse) => {
    if (user.revision < authoritativeRevisionRef.current) return false;
    authoritativeRevisionRef.current = user.revision;
    const nextValues = createProfileFormValues(user);
    setProfileUser(user);
    setInitialValues(nextValues);
    setFormValues(nextValues);
    setSession(updateSessionUserSummary(user));
    dispatch(
      accountApi.util.upsertQueryData("getCurrentUser", undefined, user),
    );
    setAvatarFile(null);
    return true;
  };

  useEffect(() => {
    const nextUser = profileQuery.currentData;
    if (
      !nextUser ||
      !profileUser ||
      nextUser.revision <= profileUser.revision
    ) {
      return;
    }
    if (isSavingProfile || isProfileDirty) {
      authoritativeRevisionRef.current = Math.max(
        authoritativeRevisionRef.current,
        nextUser.revision,
      );
      setConflictUser(nextUser);
      setProfileError(
        "Your profile changed in another session. Your draft is still here.",
      );
      return;
    }
    applyAuthoritativeProfile(nextUser);
    setConflictUser(null);
    setProfileError(null);
  }, [isProfileDirty, isSavingProfile, profileQuery.currentData, profileUser]);

  const saveProfile = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (
      !isProfileDirty ||
      !profileUser ||
      isSavingProfile ||
      conflictUser !== null
    ) {
      return;
    }

    if (!formValues.displayName.trim()) {
      setProfileError("Enter your full name.");
      return;
    }
    const phoneNumber = formValues.phoneNumber || null;
    setIsSavingProfile(true);
    setProfileFeedback(null);
    setProfileError(null);
    try {
      const user = await authService.updateMeProfile({
        expectedRevision: profileUser.revision,
        displayName: formValues.displayName,
        biography: formValues.biography,
        phoneCountryIso2: phoneNumber ? formValues.countryIso2 : null,
        phoneNumber,
        ...(avatarFile
          ? { avatarDataUrl: await createProfileAvatarDataUrl(avatarFile) }
          : {}),
      });
      if (applyAuthoritativeProfile(user)) {
        setConflictUser(null);
        setProfileFeedback("Profile saved.");
      }
    } catch (error) {
      const latest = profileConflictUser(error);
      if (latest) {
        authoritativeRevisionRef.current = Math.max(
          authoritativeRevisionRef.current,
          latest.revision,
        );
        setConflictUser((current) =>
          current && current.revision > latest.revision ? current : latest,
        );
        setProfileError(
          "Your profile changed in another session. Your draft is still here.",
        );
      } else {
        setProfileError(
          getApiErrorMessage(error, "Zenith Team could not save your profile."),
        );
      }
    } finally {
      setIsSavingProfile(false);
    }
  };

  const handleLoadLatest = () => {
    if (!conflictUser) {
      return;
    }
    if (applyAuthoritativeProfile(conflictUser)) {
      setConflictUser(null);
      setProfileError(null);
      setProfileFeedback("Latest profile loaded.");
    }
  };

  const handleCancel = () => {
    if (conflictUser) {
      handleLoadLatest();
      return;
    }
    setFormValues(initialValues);
    setAvatarFile(null);
    setProfileError(null);
    setProfileFeedback(null);
  };

  const handleSignOut = async () => {
    setIsSigningOut(true);
    try {
      await logoutZenithTeamSession();
      navigate("/login", { replace: true });
    } finally {
      setIsSigningOut(false);
    }
  };

  const handleDeleteDialogOpenChange = (open: boolean) => {
    if (isDeleting) {
      return;
    }
    setDeleteDialogOpen(open);
    if (!open) {
      setDeleteConfirmation("");
      setDeleteError(null);
    }
  };

  const handleDeleteAccount = async () => {
    if (deleteConfirmation !== "DELETE" || isDeleting) {
      return;
    }

    setIsDeleting(true);
    setDeleteError(null);
    try {
      await deleteZenithTeamAccount();
      setDeleteDialogOpen(false);
      navigate("/login", { replace: true });
    } catch (error) {
      setDeleteError(
        error instanceof Error
          ? error.message
          : "Zenith Team could not delete your account.",
      );
    } finally {
      setIsDeleting(false);
    }
  };

  return (
    <div className="my-account-page">
      <section
        className="my-account-section"
        aria-labelledby="account-profile-title"
      >
        <h2 id="account-profile-title">Profile</h2>
        <div className="my-account-identity flex items-center gap-[14px] mb-[24px]">
          <AvatarImagePicker
            label="Change your avatar"
            className="my-account-avatar flex-none w-[48px] h-[48px] rounded-[50%]"
            file={avatarFile}
            disabled={profileFieldsDisabled}
            onSelect={(file) => {
              setAvatarFile(file);
              setProfileError(null);
              setProfileFeedback(null);
            }}
            onError={(message) => {
              setProfileError(message);
              setProfileFeedback(null);
            }}
          >
            {(previewUrl) => <img src={previewUrl ?? avatarUrl} alt="" />}
          </AvatarImagePicker>
          <div>
            <strong>{profileUser?.displayName ?? session.displayName}</strong>
            <div className="mt-[3px] text-workspace-muted text-[length:13px] wrap-anywhere">
              {profileUser?.email ?? session.email}
            </div>
          </div>
        </div>
        <form
          className="my-account-form"
          onSubmit={(event) => void saveProfile(event)}
          aria-busy={isSavingProfile || isLoadingProfile}
        >
          <div className="my-account-fields">
            <div className="my-account-field grid [align-content:start] min-w-0 gap-[8px]">
              <label htmlFor="profile-display-name">Full name</label>
              <input
                autoComplete="name"
                id="profile-display-name"
                type="text"
                required
                maxLength={120}
                disabled={profileFieldsDisabled}
                value={formValues.displayName}
                onChange={(event) =>
                  updateField("displayName", event.target.value)
                }
              />
            </div>
            <div className="my-account-field grid [align-content:start] min-w-0 gap-[8px]">
              <label htmlFor="profile-phone-number">
                Phone number <span>Optional</span>
              </label>
              <div className="grid grid-cols-[minmax(0,_0.9fr)_minmax(0,_1.1fr)] gap-[8px]">
                <div className="my-account-country relative min-w-0">
                  <select
                    aria-label="Country calling code"
                    autoComplete="country"
                    disabled={profileFieldsDisabled}
                    id="profile-country-code"
                    value={formValues.countryIso2}
                    onChange={(event) =>
                      updateField("countryIso2", event.target.value)
                    }
                  >
                    {COUNTRY_CALLING_CODES.map(
                      ([iso2, country, callingCode]) => (
                        <option key={iso2} value={iso2}>
                          {callingCode} · {country}
                        </option>
                      ),
                    )}
                  </select>
                  <ChevronDown size={14} aria-hidden="true" />
                </div>
                <input
                  autoComplete="tel-national"
                  disabled={profileFieldsDisabled}
                  id="profile-phone-number"
                  inputMode="numeric"
                  placeholder="Phone number"
                  type="tel"
                  value={formValues.phoneNumber}
                  onChange={(event) => handlePhoneChange(event.target.value)}
                />
              </div>
            </div>
          </div>
          <div className="my-account-field grid [align-content:start] min-w-0 gap-[8px]">
            <label htmlFor="profile-biography">
              Bio <span>Private</span>
            </label>
            <textarea
              id="profile-biography"
              aria-describedby="profile-biography-limit"
              disabled={profileFieldsDisabled}
              placeholder="A little context about you"
              rows={4}
              value={formValues.biography}
              onChange={(event) => handleBiographyChange(event.target.value)}
            />
            <p
              className="m-0 text-workspace-muted text-[length:12px] leading-[16px] text-right"
              id="profile-biography-limit"
            >
              {biographyLineCount}/12 lines
              <span className="sr-only"> · Up to 1,200 characters</span>
            </p>
          </div>
          <div className="my-account-form-footer">
            <div className="min-w-0">
              {profileError || profileFeedback || conflictUser ? (
                <p
                  className={cx(
                    "my-account-feedback",
                    (profileError || conflictUser) && "is-error",
                  )}
                  role={profileError || conflictUser ? "alert" : "status"}
                >
                  {profileError ??
                    (conflictUser
                      ? "Your profile changed in another session. Your draft is still here."
                      : profileFeedback)}
                  {conflictUser ? (
                    <button
                      className="my-account-text-action"
                      type="button"
                      disabled={isSavingProfile}
                      onClick={handleLoadLatest}
                    >
                      Load latest
                    </button>
                  ) : !profileUser && profileQuery.isError ? (
                    <button
                      className="my-account-text-action"
                      type="button"
                      disabled={profileQuery.isFetching}
                      onClick={() => void profileQuery.refetch()}
                    >
                      Retry
                    </button>
                  ) : null}
                </p>
              ) : isLoadingProfile ? (
                <p className="my-account-feedback" role="status">
                  Loading profile…
                </p>
              ) : null}
            </div>
            <div className="my-account-actions">
              <SettingsButton
                disabled={(!isProfileDirty && !conflictUser) || isSavingProfile}
                onClick={handleCancel}
              >
                Cancel
              </SettingsButton>
              <SettingsButton
                className="my-account-save-button"
                variant={isProfileDirty ? "primary" : "secondary"}
                disabled={
                  !isProfileDirty ||
                  isSavingProfile ||
                  !profileUser ||
                  !!conflictUser
                }
                type="submit"
              >
                {isSavingProfile ? "Saving…" : "Save profile"}
              </SettingsButton>
            </div>
          </div>
        </form>
      </section>
      <section
        className="my-account-section"
        aria-labelledby="account-preferences-title"
      >
        <h2 id="account-preferences-title">Preferences</h2>
        <dl className="my-account-timezone">
          <div className="my-account-setting-row">
            <dt>
              <strong>Time zone</strong>
              <span>
                {session.user?.timezone
                  ? "Saved with your account"
                  : "Detected from this computer"}
              </span>
            </dt>
            <dd>
              {session.user?.timezone ??
                Intl.DateTimeFormat().resolvedOptions().timeZone}
            </dd>
          </div>
        </dl>
        {session.company?.id && canManageSchedules ? (
          <ProfileDailyBriefSection companyId={session.company.id} />
        ) : null}
      </section>
      {session.company?.id ? (
        <section
          className="my-account-section"
          aria-labelledby="account-companies-title"
        >
          <h2 id="account-companies-title">Your companies</h2>
          <div className="my-account-company">
            <WorkspaceCompanySwitcher
              companyId={session.company.id}
              companyName={session.company.workspaceName ?? "Company"}
              description={`${session.company.role} · Current`}
              actionLabel="Switch company"
            />
          </div>
          <section
            className="my-account-activity pt-[18px] [border-top:1px_solid_var(--workspace-divider)]"
            aria-labelledby="activity-insight-title"
          >
            <header>
              <h3 id="activity-insight-title">
                Your activity in{" "}
                {session.company.workspaceName ?? "this company"}
              </h3>
              <span>All time</span>
            </header>
            <dl
              className="my-account-metrics"
              aria-busy={activityInsight.isLoading}
            >
              <ActivityMetric
                label="Most used model"
                loading={activityInsight.isLoading}
                primary={activityInsight.isError ? "—" : (activityModel ?? "—")}
                status={
                  !activityInsight.isError &&
                  !activityInsight.data?.mostUsedModel
                    ? "No usage yet"
                    : undefined
                }
              />
              <ActivityMetric
                label="Successful tasks"
                loading={activityInsight.isLoading}
                primary={
                  activityInsight.isError
                    ? "—"
                    : formatActivityCount(
                        activityInsight.data?.successfulTasks ?? "0",
                      )
                }
              />
              <ActivityMetric
                label="Sessions"
                loading={activityInsight.isLoading}
                primary={
                  activityInsight.isError
                    ? "—"
                    : formatActivityCount(
                        activityInsight.data?.totalSessions ?? "0",
                      )
                }
              />
            </dl>
            <Link className="my-account-text-action" to="/analytics?scope=mine">
              Open Analytics
            </Link>
            {activityInsight.isError ? (
              <p className="my-account-feedback is-error" role="alert">
                Activity is unavailable.
                <button
                  className="my-account-text-action"
                  type="button"
                  onClick={activityInsight.retry}
                >
                  Retry
                </button>
              </p>
            ) : null}
          </section>
        </section>
      ) : null}
      <section
        className="my-account-section flex flex-wrap items-center justify-end gap-[16px]"
        aria-label="Account"
      >
        <div className="my-account-actions">
          <SettingsButton
            disabled={isSigningOut}
            onClick={() => void handleSignOut()}
          >
            <LogOut aria-hidden="true" size={16} />
            {isSigningOut ? "Signing out…" : "Sign out"}
          </SettingsButton>
          <Dialog
            open={deleteDialogOpen}
            onOpenChange={handleDeleteDialogOpenChange}
          >
            <DialogTrigger asChild>
              <button className="my-account-delete" type="button">
                <Icon
                  name="delete"
                  aria-hidden="true"
                  className="size-[18px] shrink-0"
                />
                <span>Delete account</span>
              </button>
            </DialogTrigger>
            <DialogContent
              className="profile-delete-dialog"
              overlayClassName="profile-delete-dialog__overlay"
              showCloseButton={false}
            >
              <div className="profile-delete-dialog__surface">
                <DialogHeader className="profile-delete-dialog__header">
                  <span className="profile-delete-dialog__icon">
                    <Icon name="delete" />
                  </span>
                  <DialogTitle>Delete your account?</DialogTitle>
                  <DialogDescription>
                    You’ll lose access to your Zenith Team account and be signed
                    out everywhere. Your data is retained, and support or an
                    administrator can restore the account.
                  </DialogDescription>
                </DialogHeader>

                <label className="profile-delete-dialog__confirmation">
                  <span>
                    Type <strong>DELETE</strong> to confirm
                  </span>
                  <input
                    autoCapitalize="characters"
                    autoComplete="off"
                    autoCorrect="off"
                    disabled={isDeleting}
                    placeholder="DELETE"
                    spellCheck={false}
                    value={deleteConfirmation}
                    onChange={(event) => {
                      setDeleteConfirmation(event.target.value);
                      setDeleteError(null);
                    }}
                  />
                </label>

                {deleteError ? (
                  <p className="profile-delete-dialog__error" role="alert">
                    {deleteError}
                  </p>
                ) : null}

                <DialogFooter className="profile-delete-dialog__footer">
                  <DialogClose asChild>
                    <button
                      className="profile-delete-dialog__button profile-delete-dialog__button--cancel"
                      disabled={isDeleting}
                      type="button"
                    >
                      Cancel
                    </button>
                  </DialogClose>
                  <button
                    className="profile-delete-dialog__button profile-delete-dialog__button--delete"
                    disabled={deleteConfirmation !== "DELETE" || isDeleting}
                    type="button"
                    onClick={() => void handleDeleteAccount()}
                  >
                    {isDeleting ? "Deleting…" : "Delete account"}
                  </button>
                </DialogFooter>
              </div>
            </DialogContent>
          </Dialog>
        </div>
      </section>
    </div>
  );
}

export default ProfileSettingsPage;
