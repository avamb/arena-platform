/**
 * Seat hold — the superadmin screen of spec 08_architecture/67, section 7.2.
 *
 *   GET /v1/admin/hold-policy
 *   PUT /v1/admin/hold-policy/{profile}
 *
 * Both profiles (standard and high_demand) as editable forms. The person types minutes, the wire carries
 * seconds (holdPolicy.ts). Each profile is saved on its own and needs a reason (X-Admin-Reason). The server
 * checks the limits again and writes the audit row. Mock data: NONE.
 */
import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError } from "@/lib/api/client";
import {
  buildHoldRequest,
  formFromProfile,
  getHoldPolicy,
  putHoldPolicy,
  type HoldField,
  type HoldFieldError,
  type HoldForm,
  type HoldPolicyResponse,
  type HoldProfile,
} from "@/lib/api/holdPolicy";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { useTranslation } from "@/lib/i18n/I18nContext";
import {
  errorStyle,
  fieldErrorStyle,
  headerRowStyle,
  headingStyle,
  hintStyle,
  inputStyle,
  labelStyle,
  mutedStyle,
  noticeStyle,
  pageStyle,
  primaryButtonStyle,
  disabledButtonStyle,
  sectionStyle,
  sectionTitleStyle,
  subheadingStyle,
} from "./onboardingUi";
import type { Translate } from "./analyticsUi";

const HOLD_POLICY_NAV_ENTRY = NAV_BY_PATH["/hold-policy"]!;

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/hold-policy",
  component: HoldPolicyRoute,
});

function HoldPolicyRoute() {
  return (
    <RequirePermission entry={HOLD_POLICY_NAV_ENTRY}>
      <HoldPolicyPage />
    </RequirePermission>
  );
}

const FORM_FIELDS: readonly (keyof HoldForm)[] = [
  "select_minutes",
  "payment_minutes",
  "extend_minutes",
  "extend_count",
  "paying_minutes",
  "max_seats_per_hold",
  "max_active_per_device",
  "max_active_per_ip",
  "rehold_pause_minutes",
  "poll_interval_seconds",
];

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : String(err);
}

function HoldPolicyPage(): JSX.Element {
  const { t } = useTranslation();
  const policy = useQuery<HoldPolicyResponse, ApiError>({
    queryKey: ["admin", "hold-policy"],
    queryFn: getHoldPolicy,
    retry: false,
    refetchOnWindowFocus: false,
  });
  return (
    <div style={pageStyle} data-testid="hold-policy">
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle}>{t("holdPolicy.title")}</h1>
          <p style={subheadingStyle}>{t("holdPolicy.subtitle")}</p>
        </div>
      </div>
      <p style={hintStyle}>{t("holdPolicy.profileHint")}</p>
      {policy.isLoading ? <p style={mutedStyle}>{t("holdPolicy.loading")}</p> : null}
      {policy.error ? (
        <div style={errorStyle} role="alert" data-testid="hold-policy-error">
          {t("holdPolicy.loadError", { message: policy.error.message })}
        </div>
      ) : null}
      {policy.data?.profiles.map((p) => (
        <ProfileSection key={`${p.profile}:${p.updated_at ?? ""}`} profile={p} />
      ))}
    </div>
  );
}

/** One profile: owns its form and its reason, saves on its own. */
function ProfileSection({ profile }: { profile: HoldProfile }): JSX.Element {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [form, setForm] = useState<HoldForm>(() => formFromProfile(profile));
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<readonly HoldFieldError[]>([]);
  const [serverError, setServerError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const save = useMutation({
    mutationFn: (v: { request: Parameters<typeof putHoldPolicy>[1]; reason: string }) =>
      putHoldPolicy(profile.profile, v.request, v.reason),
    onSuccess: () => {
      setSaved(true);
      setServerError(null);
      setReason("");
      void qc.invalidateQueries({ queryKey: ["admin", "hold-policy"] });
    },
    onError: (e) => {
      setSaved(false);
      setServerError(messageOf(e));
    },
  });

  const submit = (): void => {
    setSaved(false);
    setServerError(null);
    const built = buildHoldRequest(form, reason);
    if (!built.ok) {
      setErrors(built.errors);
      return;
    }
    setErrors([]);
    save.mutate({ request: built.request, reason: built.reason });
  };

  return (
    <HoldProfileForm
      profile={profile}
      form={form}
      reason={reason}
      errors={errors}
      serverError={serverError}
      saved={saved}
      saving={save.isPending}
      t={t}
      onField={(field, value) => {
        setSaved(false);
        setForm((f) => ({ ...f, [field]: value }));
      }}
      onReason={setReason}
      onSubmit={submit}
    />
  );
}

export interface HoldProfileFormProps {
  profile: HoldProfile;
  form: HoldForm;
  reason: string;
  errors: readonly HoldFieldError[];
  serverError: string | null;
  saved: boolean;
  saving: boolean;
  t: Translate;
  onField: (field: keyof HoldForm, value: string) => void;
  onReason: (value: string) => void;
  onSubmit: () => void;
}

/** The editable form of one profile. Router- and query-free, so tests render it with renderToStaticMarkup. */
export function HoldProfileForm(props: HoldProfileFormProps): JSX.Element {
  const { profile, form, reason, errors, t } = props;
  const errorOf = (field: HoldField): HoldFieldError | undefined => errors.find((e) => e.field === field);
  const name = profile.profile;
  const known = name === "standard" || name === "high_demand";
  return (
    <form
      style={sectionStyle}
      data-testid={`hold-profile-${name}`}
      onSubmit={(e) => {
        e.preventDefault();
        props.onSubmit();
      }}
      noValidate
    >
      <h2 style={sectionTitleStyle}>{known ? t(`holdPolicy.profile.${name}`) : name}</h2>
      {profile.updated_at !== undefined && profile.updated_at !== "" ? (
        <p style={hintStyle}>{t("holdPolicy.updatedAt", { at: profile.updated_at })}</p>
      ) : null}
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(260px, 1fr))", gap: 12 }}>
        {FORM_FIELDS.map((field) => {
          const err = errorOf(field);
          return (
            <div key={field}>
              <label style={labelStyle} htmlFor={`hold-${name}-${field}`}>
                {t(`holdPolicy.field.${field}`)}
              </label>
              <input
                id={`hold-${name}-${field}`}
                style={inputStyle}
                type="text"
                inputMode="decimal"
                value={form[field]}
                aria-invalid={err !== undefined}
                data-testid={`hold-${name}-${field}`}
                onChange={(e) => props.onField(field, e.target.value)}
              />
              {err !== undefined ? (
                <p style={fieldErrorStyle} role="alert" data-testid={`hold-${name}-${field}-error`}>
                  {t(err.key, err.params)}
                </p>
              ) : null}
            </div>
          );
        })}
      </div>
      <div style={{ marginTop: 12, maxWidth: 520 }}>
        <label style={labelStyle} htmlFor={`hold-${name}-reason`}>
          {t("holdPolicy.reason")}
        </label>
        <input
          id={`hold-${name}-reason`}
          style={inputStyle}
          type="text"
          value={reason}
          aria-invalid={errorOf("reason") !== undefined}
          data-testid={`hold-${name}-reason`}
          onChange={(e) => props.onReason(e.target.value)}
        />
        <p style={hintStyle}>{t("holdPolicy.reasonHint")}</p>
        {errorOf("reason") !== undefined ? (
          <p style={fieldErrorStyle} role="alert" data-testid={`hold-${name}-reason-error`}>
            {t(errorOf("reason")!.key)}
          </p>
        ) : null}
      </div>
      {props.serverError !== null ? (
        <div style={errorStyle} role="alert" data-testid={`hold-${name}-server-error`}>
          {t("holdPolicy.saveError", { message: props.serverError })}
        </div>
      ) : null}
      {props.saved ? (
        <div style={noticeStyle} role="status" data-testid={`hold-${name}-saved`}>
          {t("holdPolicy.saved")}
        </div>
      ) : null}
      <button
        type="submit"
        style={props.saving ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
        disabled={props.saving}
        data-testid={`hold-${name}-save`}
      >
        {props.saving ? t("holdPolicy.saving") : t("holdPolicy.save")}
      </button>
    </form>
  );
}
