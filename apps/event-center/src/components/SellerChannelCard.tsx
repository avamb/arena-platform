/**
 * 4-7: one sales-page channel of the organization. It shows whether the
 * page shows the seller block (shown, incomplete, hidden by the operator)
 * and lets the owner set the channel's public e-mail and phone through
 * PATCH .../channels/{channel_id}/seller-display.
 *
 * `seller_hidden` is the platform operator's switch: the web only SHOWS it
 * and never sends it. The page shows the block by itself once the data is
 * complete, so there is no show/hide button here.
 */
import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { SellerErrorNotice } from "@/components/SellerErrorNotice";
import { TextField } from "@/components/FormField";
import { Badge } from "@/components/ui";
import { patchSellerDisplay } from "@/lib/api/endpoints";
import type { SellerProfileChannel } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import {
  buildContactPatch,
  looksPersonalEmail,
  missingKey,
  sellerState,
  validateContact,
  type ContactForm,
  type ContactProblem,
} from "@/lib/orgData";

const TONE = { shown: "ok", incomplete: "wait", operator_hidden: "neutral" } as const;

export function SellerChannelCard({ orgId, channel }: { orgId: string; channel: SellerProfileChannel }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const stored: ContactForm = { email: channel.public_contact?.email ?? "", phone: channel.public_contact?.phone ?? "" };
  const [form, setForm] = useState<ContactForm>(stored);
  const [problems, setProblems] = useState<ContactProblem[]>([]);
  const [note, setNote] = useState<"saved" | "nothing" | null>(null);

  // The stored contact changed (a save, another tab): show it.
  useEffect(() => {
    setForm({ email: channel.public_contact?.email ?? "", phone: channel.public_contact?.phone ?? "" });
  }, [channel.public_contact?.email, channel.public_contact?.phone]);

  const save = useMutation({
    mutationFn: (contact: { email?: string | null; phone?: string | null }) => patchSellerDisplay(orgId, channel.id, contact),
    onSuccess: (profile) => {
      queryClient.setQueryData(["sellerProfile", orgId], profile);
      setNote("saved");
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    save.reset();
    const found = validateContact(form);
    setProblems(found);
    if (found.length > 0) {
      setNote(null);
      return;
    }
    const patch = buildContactPatch(stored, form);
    if (Object.keys(patch).length === 0) {
      setNote("nothing");
      return;
    }
    setNote(null);
    save.mutate(patch);
  };

  const state = sellerState(channel);
  const err = (field: "email" | "phone") => {
    const p = problems.find((x) => x.field === field);
    return p === undefined ? undefined : t(p.key);
  };
  const missing = channel.missing.map((n) => missingKey(n)).filter((k): k is NonNullable<ReturnType<typeof missingKey>> => k !== null);
  const personal = channel.email_looks_personal && form.email.trim() === stored.email.trim();

  return (
    <li className="card" data-testid="seller-channel" data-channel-name={channel.name} data-seller-state={state}>
      <h3>{channel.name}</h3>
      <p>
        <Badge tone={TONE[state]}>
          <span data-testid="seller-state">{t(`orgData.page.${state === "operator_hidden" ? "hidden" : state}`)}</span>
        </Badge>
      </p>
      {missing.length > 0 && state !== "operator_hidden" && (
        <>
          <p className="muted">{t("orgData.page.missing")}</p>
          <ul className="plain-list" data-testid="seller-channel-missing">
            {missing.map((k) => (
              <li key={k}>{t(k)}</li>
            ))}
          </ul>
        </>
      )}
      <p className="muted">{t("orgData.page.where")}</p>
      <form className="channel-form" onSubmit={onSubmit} noValidate aria-label={channel.name}>
        <TextField
          label={t("orgData.page.email")}
          type="email"
          value={form.email}
          onChange={(v) => {
            setForm((f) => ({ ...f, email: v }));
            setNote(null);
            setProblems((p) => p.filter((x) => x.field !== "email"));
          }}
          error={err("email")}
          testId="seller-email"
        />
        <TextField
          label={t("orgData.page.phone")}
          hint={t("orgData.page.phone.hint")}
          type="tel"
          value={form.phone}
          onChange={(v) => {
            setForm((f) => ({ ...f, phone: v }));
            setNote(null);
            setProblems((p) => p.filter((x) => x.field !== "phone"));
          }}
          error={err("phone")}
          testId="seller-phone"
        />
        {personal && looksPersonalEmail(stored.email) && (
          <p className="notice" role="status" data-testid="seller-personal-hint">
            {t("orgData.page.personal")}
          </p>
        )}
        {save.error !== null && <SellerErrorNotice error={save.error} testId="seller-save-error" />}
        {note === "saved" && (
          <p className="notice notice-ok" role="status" data-testid="seller-saved">
            {t("orgData.saved")}
          </p>
        )}
        {note === "nothing" && (
          <p className="notice" role="status" data-testid="seller-nothing">
            {t("orgData.nothingChanged")}
          </p>
        )}
        <button type="submit" className="btn" disabled={save.isPending} data-testid="seller-save">
          {save.isPending ? t("orgData.saving") : t("orgData.page.save")}
        </button>
      </form>
    </li>
  );
}
