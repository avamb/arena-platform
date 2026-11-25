/**
 * "Quick payment in the widget" of the organization's Stripe config: the
 * publishable key that lets buyers pay on the page itself (card, Apple Pay,
 * Google Pay). Everybody who reads the configs sees the state; only a person
 * with `payment_config.write` sees the form. The server stays the authority
 * (it asks Stripe whether the key is real); the web only checks the prefix
 * against the config's mode to save a round trip.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { ApiError } from "@/lib/api/http";
import { setStripePublishableKey } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { hasPublishableKey, publishableKeyProblem } from "@/lib/orgData";

export interface QuickPayConfig {
  id: string;
  mode: "test" | "live";
  secret_fields_set: readonly string[];
}

export function StripeQuickPay({ orgId, config, canWrite }: { orgId: string; config: QuickPayConfig; canWrite: boolean }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const has = hasPublishableKey(config.secret_fields_set);
  const [replacing, setReplacing] = useState(false);
  const [value, setValue] = useState("");
  const [problem, setProblem] = useState<ReturnType<typeof publishableKeyProblem>>(null);
  const save = useMutation({
    mutationFn: (key: string) => setStripePublishableKey(orgId, config.id, key),
    onSuccess: async () => {
      setValue("");
      setReplacing(false);
      await queryClient.invalidateQueries({ queryKey: ["paymentConfigs", orgId] });
    },
  });

  const submit = () => {
    const p = publishableKeyProblem(config.mode, value);
    setProblem(p);
    if (p === null) save.mutate(value.trim());
  };
  const showForm = canWrite && (!has || replacing);
  const err = save.error;

  return (
    <div data-testid="quick-pay" data-config-id={config.id} data-on={has ? "true" : "false"} data-mode={config.mode}>
      <h3>
        {t("orgData.quickPay.title")} ({t(config.mode === "live" ? "orgData.quickPay.mode.live" : "orgData.quickPay.mode.test")})
      </h3>
      {has && (
        <p className="notice notice-ok" role="status" data-testid="quick-pay-on">
          {t("orgData.quickPay.on")}
        </p>
      )}
      {has && canWrite && !replacing && (
        <p>
          <button type="button" className="btn btn-small" onClick={() => setReplacing(true)} data-testid="quick-pay-replace">
            {t("orgData.quickPay.replace")}
          </button>
        </p>
      )}
      {showForm && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
          data-testid="quick-pay-form"
        >
          {!has && <p data-testid="quick-pay-hint">{t("orgData.quickPay.hint")}</p>}
          <label>
            {t("orgData.quickPay.label")}
            <input
              type="text"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              placeholder={config.mode === "live" ? "pk_live_…" : "pk_test_…"}
              data-testid="quick-pay-input"
            />
          </label>
          {problem !== null && (
            <p className="notice notice-bad" role="alert" data-testid="quick-pay-problem">
              {t(`orgData.quickPay.err.${problem}`)}
            </p>
          )}
          {err !== null && err !== undefined && <QuickPayError error={err} />}
          <p>
            <button type="submit" className="btn" disabled={save.isPending} data-testid="quick-pay-save">
              {t("orgData.quickPay.save")}
            </button>{" "}
            {has && (
              <button type="button" className="btn btn-small" onClick={() => setReplacing(false)} data-testid="quick-pay-cancel">
                {t("orgData.quickPay.cancel")}
              </button>
            )}
          </p>
        </form>
      )}
    </div>
  );
}

function QuickPayError({ error }: { error: unknown }) {
  const { t } = useI18n();
  if (error instanceof ApiError) {
    const text = error.code === "payment_config.publishable_key_refused" ? t("orgData.quickPay.err.refused") : error.message;
    return (
      <p className="notice notice-bad" role="alert" data-testid="quick-pay-error">
        {text}
      </p>
    );
  }
  return <ErrorNotice error={error} testId="quick-pay-error" />;
}
