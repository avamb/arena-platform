/**
 * The buyer of an order in one line: initials, name, e-mail, phone, and two
 * round buttons to call and to write on WhatsApp (owner decision 2026-10-30,
 * the WordPress ops center's card). The buttons show only when the order
 * carries a phone number. Nothing here is fetched: the order card has it all.
 */
import type { OrderDetail } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";

/** Up to two initials from the name, else the first letter of the e-mail. */
export function buyerInitials(name: string | null, email: string | null): string {
  const words = (name ?? "").trim().split(/\s+/).filter((w) => w !== "");
  if (words.length > 0) {
    const first = Array.from(words[0] ?? "")[0] ?? "";
    const last = words.length > 1 ? (Array.from(words[words.length - 1] ?? "")[0] ?? "") : "";
    return (first + last).toUpperCase();
  }
  const e = Array.from((email ?? "").trim())[0];
  return e !== undefined ? e.toUpperCase() : "?";
}

/** The number a tel: link dials: digits and a leading plus. */
export function telHref(phone: string): string {
  return `tel:${phone.replace(/[^\d+]/g, "")}`;
}

/**
 * The wa.me link for a phone number, or null when the number is too short to
 * be an international one. WhatsApp wants the digits with the country code and
 * no plus; a leading 00 is the plus written out.
 */
export function whatsAppHref(phone: string): string | null {
  let digits = phone.replace(/\D/g, "");
  if (!phone.trim().startsWith("+") && digits.startsWith("00")) digits = digits.slice(2);
  return digits.length >= 8 ? `https://wa.me/${digits}` : null;
}

function PhoneIcon() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M22 16.92v3a2 2 0 0 1-2.18 2 19.79 19.79 0 0 1-8.63-3.07 19.5 19.5 0 0 1-6-6 19.79 19.79 0 0 1-3.07-8.67A2 2 0 0 1 4.11 2h3a2 2 0 0 1 2 1.72 12.84 12.84 0 0 0 .7 2.81 2 2 0 0 1-.45 2.11L8.09 9.91a16 16 0 0 0 6 6l1.27-1.27a2 2 0 0 1 2.11-.45 12.84 12.84 0 0 0 2.81.7A2 2 0 0 1 22 16.92z" />
    </svg>
  );
}

function ChatIcon() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z" />
    </svg>
  );
}

export function BuyerContact({ order }: { order: Pick<OrderDetail, "buyer_name" | "buyer_email" | "buyer_phone"> }) {
  const { t } = useI18n();
  const { buyer_name: name, buyer_email: email, buyer_phone: phone } = order;
  if (name === null && email === null && phone === null) {
    return (
      <p className="muted" data-testid="buyer">
        {t("order.noContacts")}
      </p>
    );
  }
  const wa = phone !== null ? whatsAppHref(phone) : null;
  return (
    <div className="buyer" data-testid="buyer">
      <span className="avatar" aria-hidden="true">
        {buyerInitials(name, email)}
      </span>
      <div className="buyer-info">
        {name !== null && <span className="buyer-name">{name}</span>}
        {email !== null && (
          <a href={`mailto:${encodeURIComponent(email)}`} className="buyer-email">
            {email}
          </a>
        )}
        {phone !== null && (
          <a href={telHref(phone)} className="buyer-phone">
            {phone}
          </a>
        )}
      </div>
      {phone !== null && (
        <span className="buyer-actions">
          <a href={telHref(phone)} className="round-btn round-btn-call" aria-label={t("contact.call", { phone })} data-testid="buyer-call">
            <PhoneIcon />
          </a>
          {wa !== null && (
            <a
              href={wa}
              className="round-btn round-btn-wa"
              target="_blank"
              rel="noopener noreferrer"
              aria-label={t("contact.whatsapp", { phone })}
              data-testid="buyer-whatsapp"
            >
              <ChatIcon />
            </a>
          )}
        </span>
      )}
    </div>
  );
}
