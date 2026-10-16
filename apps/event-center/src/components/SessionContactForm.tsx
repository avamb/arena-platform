/**
 * 5-2: the organizer contact buyers answer to, asked INSIDE the session
 * change when the dry run says `contact_missing` (the bot's `sesSaveContact`).
 * The e-mail is stored for the event's promoter, else for the organization
 * (the server decides; `contact.target_name` says which). Saving does not
 * change the session: the caller asks the dry run again.
 *
 * Only a person who may edit the event (event.update) gets the form; anyone
 * else is told whom to ask. The address is never logged and never put in a URL.
 */
import { useId, useRef, useState } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { setEventContact } from "@/lib/api/endpoints";
import type { OrganizerContact } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { validEmail } from "@/lib/invitations";

export interface SessionContactFormProps {
  orgId: string;
  eventId: string;
  contact: OrganizerContact;
  /** The person holds event.update. */
  canEdit: boolean;
  /** The contact is saved: ask the dry run again. */
  onSaved: () => void;
}

export function SessionContactForm({ orgId, eventId, contact, canEdit, onSaved }: SessionContactFormProps) {
  const { t } = useI18n();
  const ids = useId();
  const [email, setEmail] = useState("");
  const [phone, setPhone] = useState("");
  const [showPhone, setShowPhone] = useState(false);
  const [badEmail, setBadEmail] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const pressed = useRef(false);

  if (!canEdit) {
    return (
      <div className="notice" data-testid="session-contact-noright">
        <p>{t("sessionChange.contact.noRight")}</p>
      </div>
    );
  }

  const submit = async () => {
    if (pressed.current) return;
    const address = email.trim();
    if (!validEmail(address)) {
      setBadEmail(true);
      return;
    }
    setBadEmail(false);
    setFailure(null);
    pressed.current = true;
    setBusy(true);
    try {
      const trimmed = phone.trim();
      await setEventContact(orgId, eventId, {
        email: address,
        phone: trimmed === "" ? null : trimmed,
        phone_hidden: trimmed === "" ? false : !showPhone,
      });
      onSaved();
    } catch (error) {
      setFailure({ error });
    } finally {
      pressed.current = false;
      setBusy(false);
    }
  };

  return (
    <form
      className="session-contact"
      aria-labelledby={`${ids}-title`}
      data-testid="session-contact-form"
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <h3 id={`${ids}-title`}>{t("sessionChange.contact.title")}</h3>
      <p>{t("sessionChange.contact.explain", { name: contact.target_name })}</p>
      <label htmlFor={`${ids}-email`}>{t("sessionChange.contact.email")}</label>
      <input
        id={`${ids}-email`}
        type="email"
        autoComplete="off"
        value={email}
        aria-invalid={badEmail ? true : undefined}
        aria-describedby={badEmail ? `${ids}-bad` : undefined}
        data-testid="session-contact-email"
        onChange={(e) => {
          setEmail(e.target.value);
          setBadEmail(false);
        }}
      />
      {badEmail && (
        <p className="field-error" id={`${ids}-bad`} role="alert" data-testid="session-contact-email-error">
          {t("sessionChange.contact.badEmail")}
        </p>
      )}
      <label htmlFor={`${ids}-phone`}>{t("sessionChange.contact.phone")}</label>
      <input
        id={`${ids}-phone`}
        type="tel"
        autoComplete="off"
        maxLength={40}
        value={phone}
        data-testid="session-contact-phone"
        onChange={(e) => setPhone(e.target.value)}
      />
      {phone.trim() !== "" && (
        <p>
          <label className="check">
            <input
              type="checkbox"
              checked={showPhone}
              data-testid="session-contact-show-phone"
              onChange={(e) => setShowPhone(e.target.checked)}
            />
            <span>{t("sessionChange.contact.showPhone")}</span>
          </label>
        </p>
      )}
      {failure !== null && <ErrorNotice error={failure.error} testId="session-contact-error" />}
      <p>
        <button type="submit" className="btn btn-primary" disabled={busy} data-testid="session-contact-save">
          {busy ? t("dryRun.working") : t("sessionChange.contact.save")}
        </button>
      </p>
    </form>
  );
}
