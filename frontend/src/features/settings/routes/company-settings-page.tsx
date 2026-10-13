import { getNextRoute } from "@/app/session/session-routing";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { ChevronDown } from "@/shared/ui/icons";
import { CompanyAvatarField } from "@/features/settings/components/company-avatar-field";
import { attachmentService } from "@/shared/api/attachment.service";
import {
  readSessionSnapshot,
  replaceDeletedSessionCompany,
  updateSessionCompanySummary,
} from "@/shared/session/session";
import { COUNTRY_CALLING_CODES } from "@/features/settings/model/country-calling-codes";
import {
  getApiErrorBody,
  getApiErrorCode,
  getApiErrorMessage,
} from "@/lib/api-client";
import {
  boardlyApi,
  useDeleteWorkspaceCompanyMutation,
  useGetWorkspaceCompanyQuery,
  useLazyListWorkspaceCompaniesQuery,
  useUpdateWorkspaceCompanyProfileMutation,
} from "@/shared/api";
import type { BoardlyApiError } from "@/shared/api/boardly-api";
import type { CompanyDetail } from "@/shared/api/types";
import { Icon } from "@/shared/ui/icon";
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
import { hasPermission } from "@/shared/permissions";
import { useAppDispatch } from "@/state";

type CompanyProfileFormValues = {
  avatarAttachmentId: string | null;
  contactEmail: string;
  countryIso2: string;
  name: string;
  phoneNumber: string;
};

const emptyCompanyProfileFormValues: CompanyProfileFormValues = {
  avatarAttachmentId: null,
  contactEmail: "",
  countryIso2: "VN",
  name: "",
  phoneNumber: "",
};

function createCompanyProfileFormValues(
  company: CompanyDetail,
): CompanyProfileFormValues {
  return {
    avatarAttachmentId: company.avatarAttachmentId ?? null,
    contactEmail: company.contactEmail ?? "",
    countryIso2: company.phoneCountryIso2 ?? "VN",
    name: company.name,
    phoneNumber: company.phoneNumber ?? "",
  };
}

function companyProfileConflict(error: unknown): CompanyDetail | null {
  const queryError = error as BoardlyApiError | null | undefined;
  const code = getApiErrorCode(error) ?? queryError?.code ?? null;
  if (code !== "company_profile_revision_conflict") {
    return null;
  }
  return (
    ((getApiErrorBody(error) ?? queryError?.body)?.currentCompany as
      CompanyDetail | undefined) ?? null
  );
}

function isPhoneDirty(
  values: CompanyProfileFormValues,
  initial: CompanyProfileFormValues,
) {
  return (
    values.phoneNumber !== initial.phoneNumber ||
    (values.phoneNumber !== "" && values.countryIso2 !== initial.countryIso2)
  );
}

export function CompanySettingsPage() {
  const session = readSessionSnapshot();
  const companyId = session.company?.id;
  const navigate = useNavigate();
  const dispatch = useAppDispatch();
  const companyQuery = useGetWorkspaceCompanyQuery(companyId ?? "", {
    skip: !companyId,
  });
  const company = companyQuery.currentData;
  const [updateCompanyProfile] = useUpdateWorkspaceCompanyProfileMutation();
  const [deleteCompany] = useDeleteWorkspaceCompanyMutation();
  const [listCompanies] = useLazyListWorkspaceCompaniesQuery();
  const [authoritativeCompany, setAuthoritativeCompany] =
    useState<CompanyDetail | null>(null);
  const [initialValues, setInitialValues] = useState(
    emptyCompanyProfileFormValues,
  );
  const [formValues, setFormValues] = useState(emptyCompanyProfileFormValues);
  const [avatarFile, setAvatarFile] = useState<File | null>(null);
  const uploadedAvatarRef = useRef<{ file: File; id: string } | null>(null);
  const [isSavingProfile, setIsSavingProfile] = useState(false);
  const [profileFeedback, setProfileFeedback] = useState<string | null>(null);
  const [profileError, setProfileError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<
    Partial<Record<keyof CompanyProfileFormValues, string>>
  >({});
  const [conflictCompany, setConflictCompany] = useState<CompanyDetail | null>(
    null,
  );
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [deleteConfirmation, setDeleteConfirmation] = useState("");
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [isDeleting, setIsDeleting] = useState(false);
  const authoritativeVersionRef = useRef<{
    companyId: string;
    revision: number;
  } | null>(null);
  const initialValuesRef = useRef(initialValues);
  useEffect(() => {
    if (!company) {
      return;
    }
    const tracked = authoritativeVersionRef.current;
    if (
      tracked?.companyId === company.id &&
      tracked.revision >= company.revision
    ) {
      return;
    }
    const nextValues = createCompanyProfileFormValues(company);
    const previousInitial = initialValuesRef.current;
    const isFirstCompany = tracked?.companyId !== company.id;
    if (isFirstCompany) {
      setAvatarFile(null);
      uploadedAvatarRef.current = null;
      setProfileFeedback(null);
      setProfileError(null);
      setFieldErrors({});
      setConflictCompany(null);
    }
    setFormValues((current) => {
      if (isFirstCompany) {
        return nextValues;
      }
      const phoneWasDirty = isPhoneDirty(current, previousInitial);
      return {
        avatarAttachmentId:
          current.avatarAttachmentId === previousInitial.avatarAttachmentId
            ? nextValues.avatarAttachmentId
            : current.avatarAttachmentId,
        contactEmail:
          current.contactEmail === previousInitial.contactEmail
            ? nextValues.contactEmail
            : current.contactEmail,
        countryIso2: phoneWasDirty
          ? current.countryIso2
          : nextValues.countryIso2,
        name:
          current.name === previousInitial.name
            ? nextValues.name
            : current.name,
        phoneNumber: phoneWasDirty
          ? current.phoneNumber
          : nextValues.phoneNumber,
      };
    });
    initialValuesRef.current = nextValues;
    setInitialValues(nextValues);
    setAuthoritativeCompany(company);
    authoritativeVersionRef.current = {
      companyId: company.id,
      revision: company.revision,
    };
  }, [company]);

  const isCurrentCompany = authoritativeCompany?.id === companyId;
  const canEditProfile =
    isCurrentCompany &&
    hasPermission(authoritativeCompany, "company.profile.update");
  const canDeleteCompany =
    isCurrentCompany && hasPermission(authoritativeCompany, "company.delete");
  const profileDisabled =
    companyQuery.isLoading || isSavingProfile || !isCurrentCompany;
  const isProfileDirty =
    avatarFile !== null ||
    formValues.avatarAttachmentId !== initialValues.avatarAttachmentId ||
    formValues.name !== initialValues.name ||
    formValues.contactEmail !== initialValues.contactEmail ||
    isPhoneDirty(formValues, initialValues);
  const phoneCountry = COUNTRY_CALLING_CODES.find(
    ([iso2]) => iso2 === formValues.countryIso2,
  );

  const updateProfileField = <Field extends keyof CompanyProfileFormValues>(
    field: Field,
    value: CompanyProfileFormValues[Field],
  ) => {
    setFormValues((current) => ({ ...current, [field]: value }));
    setFieldErrors((current) => ({ ...current, [field]: undefined }));
    setProfileFeedback(null);
    setProfileError(null);
  };

  const applyAuthoritativeCompany = (nextCompany: CompanyDetail) => {
    setAvatarFile(null);
    uploadedAvatarRef.current = null;
    const nextValues = createCompanyProfileFormValues(nextCompany);
    initialValuesRef.current = nextValues;
    setInitialValues(nextValues);
    setFormValues(nextValues);
    setAuthoritativeCompany(nextCompany);
    authoritativeVersionRef.current = {
      companyId: nextCompany.id,
      revision: nextCompany.revision,
    };
    updateSessionCompanySummary(nextCompany);
  };

  const saveCompanyProfile = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (
      !companyId ||
      !authoritativeCompany ||
      !canEditProfile ||
      !isProfileDirty ||
      isSavingProfile ||
      conflictCompany
    )
      return;

    const errors: Partial<Record<keyof CompanyProfileFormValues, string>> = {};
    const name = formValues.name.trim();
    const contactEmail = formValues.contactEmail.trim();
    const emailInput = event.currentTarget.elements.namedItem(
      "contactEmail",
    ) as HTMLInputElement;
    if (!name) errors.name = "Enter a company name.";
    else if ([...name].length > 120)
      errors.name = "Use 120 characters or fewer.";
    if (
      contactEmail &&
      (emailInput.validity.typeMismatch ||
        !contactEmail.split("@")[1]?.includes("."))
    ) {
      errors.contactEmail =
        "Enter a valid email address, such as hello@company.com.";
    }
    setFieldErrors(errors);
    if (Object.keys(errors).length) {
      (
        event.currentTarget.elements.namedItem(
          errors.name ? "name" : "contactEmail",
        ) as HTMLInputElement
      ).focus();
      return;
    }

    setIsSavingProfile(true);
    setProfileFeedback(null);
    setProfileError(null);
    try {
      let avatarAttachmentId = formValues.avatarAttachmentId;
      if (avatarFile) {
        if (uploadedAvatarRef.current?.file !== avatarFile) {
          const uploaded = await attachmentService.uploadFile(
            companyId,
            avatarFile,
          );
          uploadedAvatarRef.current = { file: avatarFile, id: uploaded.id };
        }
        avatarAttachmentId = uploadedAvatarRef.current.id;
        if (authoritativeVersionRef.current?.companyId !== companyId) return;
        setFormValues((current) => ({ ...current, avatarAttachmentId }));
      }
      const phoneNumber = formValues.phoneNumber || null;
      const nextCompany = await updateCompanyProfile({
        companyId,
        payload: {
          expectedRevision: authoritativeCompany.revision,
          avatarAttachmentId,
          name,
          contactEmail: contactEmail || null,
          phoneCountryIso2: phoneNumber ? formValues.countryIso2 : null,
          phoneNumber,
        },
      }).unwrap();
      if (authoritativeVersionRef.current?.companyId !== companyId) return;
      applyAuthoritativeCompany(nextCompany);
      setProfileFeedback("Changes saved.");
    } catch (error) {
      const latest = companyProfileConflict(error);
      if (latest) {
        setConflictCompany(latest);
        setProfileError(
          "Company settings changed in another session. Your draft is still here. Load the latest settings to review before saving.",
        );
      } else {
        setProfileError(
          getApiErrorMessage(
            error,
            "Company settings could not be saved. Please try again.",
          ),
        );
      }
    } finally {
      setIsSavingProfile(false);
    }
  };

  const cancelProfileChanges = () => {
    if (isSavingProfile) return;
    setAvatarFile(null);
    uploadedAvatarRef.current = null;
    if (conflictCompany) applyAuthoritativeCompany(conflictCompany);
    else setFormValues(initialValues);
    setConflictCompany(null);
    setFieldErrors({});
    setProfileError(null);
    setProfileFeedback(null);
  };

  const loadLatestCompany = () => {
    if (!conflictCompany) return;
    // Rebase the draft on the latest revision without discarding typed fields.
    const nextValues = createCompanyProfileFormValues(conflictCompany);
    setFormValues((current) => ({
      avatarAttachmentId:
        current.avatarAttachmentId === initialValues.avatarAttachmentId
          ? nextValues.avatarAttachmentId
          : current.avatarAttachmentId,
      name:
        current.name === initialValues.name ? nextValues.name : current.name,
      contactEmail:
        current.contactEmail === initialValues.contactEmail
          ? nextValues.contactEmail
          : current.contactEmail,
      phoneNumber: isPhoneDirty(current, initialValues)
        ? current.phoneNumber
        : nextValues.phoneNumber,
      countryIso2: isPhoneDirty(current, initialValues)
        ? current.countryIso2
        : nextValues.countryIso2,
    }));
    setAuthoritativeCompany(conflictCompany);
    updateSessionCompanySummary(conflictCompany);
    authoritativeVersionRef.current = {
      companyId: conflictCompany.id,
      revision: conflictCompany.revision,
    };
    initialValuesRef.current = nextValues;
    setInitialValues(nextValues);
    setConflictCompany(null);
    setProfileError(null);
    setProfileFeedback(
      "Latest settings loaded. Review your changes and save again.",
    );
  };

  const handleDeleteCompany = async () => {
    if (
      !companyId ||
      !authoritativeCompany ||
      !canDeleteCompany ||
      isDeleting ||
      deleteConfirmation !== authoritativeCompany.name ||
      !session.userId
    ) {
      return;
    }

    setIsDeleting(true);
    setDeleteError(null);
    try {
      // Resolve the deterministic fallback before deleting the active Company.
      // If this read fails, leave the irreversible action untouched.
      const page = await listCompanies(undefined, false).unwrap();
      const nextCompany = page.items.find((item) => item.id !== companyId);
      await deleteCompany({
        companyId,
        expectedRevision: authoritativeCompany.revision,
      }).unwrap();

      const nextSession = replaceDeletedSessionCompany(
        companyId,
        session.userId,
        nextCompany,
      );
      dispatch(boardlyApi.util.resetApiState());
      navigate(getNextRoute(nextSession ?? readSessionSnapshot()), {
        replace: true,
      });
    } catch (error) {
      setDeleteError(
        getApiErrorMessage(
          error,
          "Company could not be deleted. Refresh and try again.",
        ),
      );
    } finally {
      setIsDeleting(false);
    }
  };

  return (
    <div className="[container:settings-detail_/_inline-size] w-full min-w-0 text-workspace-text company-settings-page">
      <section
        className="settings-detail-section"
        aria-labelledby="company-profile-title"
      >
        <header className="settings-detail-heading">
          {companyId ? (
            <CompanyAvatarField
              key={companyId}
              companyId={companyId}
              name={formValues.name}
              attachmentId={formValues.avatarAttachmentId}
              file={avatarFile}
              canEdit={Boolean(canEditProfile)}
              disabled={profileDisabled}
              onSelect={(file) => {
                setAvatarFile(file);
                uploadedAvatarRef.current = null;
                setProfileError(null);
                setProfileFeedback(null);
              }}
              onError={(message) => {
                setProfileError(message);
                setProfileFeedback(null);
              }}
            />
          ) : null}
          <div>
            <h2 id="company-profile-title">Company profile</h2>
            <p>Your company's name and contact details.</p>
          </div>
        </header>
        <form
          className="company-profile-form"
          noValidate
          onSubmit={(event) => void saveCompanyProfile(event)}
        >
          <div className="settings-form-field company-profile-name">
            <label htmlFor="company-profile-name">Company name</label>
            <input
              id="company-profile-name"
              name="name"
              autoComplete="organization"
              required
              disabled={profileDisabled}
              readOnly={!canEditProfile}
              value={formValues.name}
              aria-invalid={Boolean(fieldErrors.name)}
              aria-describedby={
                fieldErrors.name ? "company-name-error" : undefined
              }
              onChange={(event) =>
                updateProfileField("name", event.target.value)
              }
            />
            {fieldErrors.name ? (
              <p
                id="company-name-error"
                className="settings-field-error"
                role="alert"
              >
                {fieldErrors.name}
              </p>
            ) : null}
          </div>
          <div className="settings-form-field">
            <label htmlFor="company-profile-email">
              Contact email <span>Optional</span>
            </label>
            <input
              id="company-profile-email"
              name="contactEmail"
              type="email"
              autoComplete="email"
              placeholder="hello@company.com"
              disabled={profileDisabled}
              readOnly={!canEditProfile}
              value={formValues.contactEmail}
              aria-invalid={Boolean(fieldErrors.contactEmail)}
              aria-describedby={
                fieldErrors.contactEmail ? "company-email-error" : undefined
              }
              onChange={(event) =>
                updateProfileField("contactEmail", event.target.value)
              }
            />
            {fieldErrors.contactEmail ? (
              <p
                id="company-email-error"
                className="settings-field-error"
                role="alert"
              >
                {fieldErrors.contactEmail}
              </p>
            ) : null}
          </div>
          <fieldset className="settings-form-field company-phone-field">
            <legend>
              Phone number <span>Optional</span>
            </legend>
            <div className="grid grid-cols-[112px_minmax(0,_1fr)] gap-[8px]">
              <span className="settings-select-wrap flex relative min-w-0 items-center company-country-select">
                <span className="company-country-code" aria-hidden="true">
                  {phoneCountry?.[2] ?? formValues.countryIso2}
                </span>
                <label
                  className="sr-only"
                  htmlFor="company-profile-country-code"
                >
                  Country calling code
                </label>
                <select
                  id="company-profile-country-code"
                  title={phoneCountry?.[1]}
                  disabled={profileDisabled || !canEditProfile}
                  value={formValues.countryIso2}
                  onChange={(event) =>
                    updateProfileField("countryIso2", event.target.value)
                  }
                >
                  {COUNTRY_CALLING_CODES.map(([iso2, country, callingCode]) => (
                    <option key={iso2} value={iso2}>
                      {callingCode} · {country}
                    </option>
                  ))}
                </select>
                <ChevronDown size={16} aria-hidden="true" />
              </span>
              <label className="sr-only" htmlFor="company-profile-phone-number">
                Company phone number
              </label>
              <input
                id="company-profile-phone-number"
                name="phoneNumber"
                type="tel"
                inputMode="numeric"
                autoComplete="tel-national"
                placeholder="Phone number"
                disabled={profileDisabled}
                readOnly={!canEditProfile}
                value={formValues.phoneNumber}
                onChange={(event) =>
                  updateProfileField(
                    "phoneNumber",
                    event.target.value.replace(/\D/g, "").slice(0, 15),
                  )
                }
              />
            </div>
          </fieldset>
          {companyQuery.isError || profileError ? (
            <div
              className="settings-form-error company-profile-error"
              role="alert"
            >
              <p>
                {companyQuery.isError
                  ? "Company settings could not be loaded."
                  : profileError}
              </p>
              {companyQuery.isError ? (
                <button
                  type="button"
                  className="settings-inline-link"
                  onClick={() => void companyQuery.refetch()}
                >
                  Retry
                </button>
              ) : null}
              {conflictCompany ? (
                <button
                  type="button"
                  className="settings-inline-link"
                  onClick={loadLatestCompany}
                >
                  Load latest settings
                </button>
              ) : null}
            </div>
          ) : null}
          {canEditProfile ? (
            <div className="settings-form-actions">
              <p role="status">
                {profileFeedback ?? (isProfileDirty ? "Unsaved changes" : "")}
              </p>
              <button
                className="workspace-action-button"
                type="button"
                disabled={!isProfileDirty || isSavingProfile}
                onClick={cancelProfileChanges}
              >
                Cancel
              </button>
              <button
                className="workspace-action-button workspace-action-button--primary"
                type="submit"
                disabled={
                  !isProfileDirty || isSavingProfile || conflictCompany !== null
                }
              >
                {isSavingProfile ? "Saving…" : "Save changes"}
              </button>
            </div>
          ) : null}
        </form>
      </section>
      {canDeleteCompany && authoritativeCompany ? (
        <section
          className="settings-detail-section company-settings-danger"
          aria-labelledby="delete-company-title"
        >
          <div>
            <h2 id="delete-company-title">Delete company</h2>
            <p>
              Permanently delete this company and remove access for all members.
            </p>
          </div>
          <Dialog
            open={deleteDialogOpen}
            onOpenChange={(open) => {
              if (isDeleting) return;
              setDeleteDialogOpen(open);
              if (!open) {
                setDeleteConfirmation("");
                setDeleteError(null);
              }
            }}
          >
            <DialogTrigger asChild>
              <button className="company-danger-zone__button" type="button">
                <Icon name="delete" aria-hidden="true" />
                Delete company
              </button>
            </DialogTrigger>
            <DialogContent
              className="profile-delete-dialog company-delete-dialog"
              overlayClassName="profile-delete-dialog__overlay"
              showCloseButton={false}
            >
              <div className="profile-delete-dialog__surface">
                <DialogHeader className="profile-delete-dialog__header">
                  <span className="profile-delete-dialog__icon">
                    <Icon name="delete" />
                  </span>
                  <DialogTitle>Delete this company permanently?</DialogTitle>
                  <DialogDescription>
                    Every member will immediately lose access. Zenith Team
                    database records are deleted permanently. Content held by
                    external providers and authored files on connected computers
                    are not remotely deleted.
                  </DialogDescription>
                </DialogHeader>

                <label className="profile-delete-dialog__confirmation">
                  <span>
                    Type <strong>{authoritativeCompany.name}</strong> to confirm
                  </span>
                  <input
                    autoComplete="off"
                    autoCorrect="off"
                    disabled={isDeleting}
                    placeholder={authoritativeCompany.name}
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
                    disabled={
                      deleteConfirmation !== authoritativeCompany.name ||
                      isDeleting
                    }
                    type="button"
                    onClick={() => void handleDeleteCompany()}
                  >
                    {isDeleting ? "Deleting…" : "Delete company"}
                  </button>
                </DialogFooter>
              </div>
            </DialogContent>
          </Dialog>
        </section>
      ) : null}
    </div>
  );
}

export default CompanySettingsPage;
