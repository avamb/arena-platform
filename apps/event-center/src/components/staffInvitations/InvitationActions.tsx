/**
 * "Download PDF" and "Send again" of one invitation (INV-03 routes). The PDF
 * holds every ticket of the order and is always there, whatever the delivery;
 * the letter can be sent again unless the invitation was PDF only.
 *
 * If the server does not have the routes yet (a 404 or 405 no handler wrote),
 * the button says so and the rest of the screen keeps working.
 */
import { useState } from "react";
import { saveFile } from "@/components/ui";
import { downloadStaffInvitationPdf, resendStaffInvitation } from "@/lib/api/endpoints";
import { describeApiError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { isMissingRoute } from "@/lib/staffInvitations";

interface Props {
  orgId: string;
  orderId: string;
  systemId: number;
  /** False while the tickets are still being issued. */
  ready?: boolean;
  canResend: boolean;
}

type Say = { text: string; bad: boolean } | null;

export function InvitationActions({ orgId, orderId, systemId, ready = true, canResend }: Props) {
  const { t } = useI18n();
  const [busy, setBusy] = useState<"pdf" | "resend" | null>(null);
  const [say, setSay] = useState<Say>(null);

  const fail = (err: unknown) => {
    if (isMissingRoute(err)) setSay({ text: t("seatInv.missing"), bad: true });
    else {
      const e = describeApiError(err, t);
      setSay({ text: `${e.title} ${e.next}`, bad: true });
    }
  };

  const pdf = async () => {
    setBusy("pdf");
    setSay(null);
    try {
      saveFile(await downloadStaffInvitationPdf(orgId, orderId, systemId));
    } catch (err) {
      fail(err);
    } finally {
      setBusy(null);
    }
  };
  const resend = async () => {
    setBusy("resend");
    setSay(null);
    try {
      await resendStaffInvitation(orgId, orderId);
      setSay({ text: t("seatInv.resend.done"), bad: false });
    } catch (err) {
      fail(err);
    } finally {
      setBusy(null);
    }
  };

  return (
    <span className="inv-actions">
      <button type="button" className="btn" disabled={!ready || busy !== null} onClick={() => void pdf()} data-testid="inv-pdf">
        {t("seatInv.pdf")}
      </button>
      {canResend && (
        <button type="button" className="btn" disabled={!ready || busy !== null} onClick={() => void resend()} data-testid="inv-resend">
          {t("seatInv.resend")}
        </button>
      )}
      <span className={say?.bad === true ? "bad-text" : "muted"} role={say?.bad === true ? "alert" : "status"} aria-live="polite" data-testid="inv-action-say">
        {say?.text ?? ""}
      </span>
    </span>
  );
}
