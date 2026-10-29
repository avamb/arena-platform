/**
 * The internal note of a shop order (A-10): "called, deliver after 18:00". Only the shop's own people see it,
 * the buyer, the letters and the storefront never do. It saves when the person leaves the field, and the line
 * under it says who wrote it and when (the author's name as it was when the note was saved).
 *
 * A reader (no `shop.manage`) sees the note as text, and sees nothing when there is none.
 */
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextAreaField } from "@/components/FormField";
import { saveShopOrderNote } from "@/lib/api/shopApi";
import type { ShopOrder } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { STAFF_NOTE_MAX, formatShopTime, staffNoteChanged } from "@/lib/shop";

export function ShopOrderNote({ orgId, order, canManage, country }: { orgId: string; order: ShopOrder; canManage: boolean; country?: string | null }) {
  const { t, locale } = useI18n();
  const client = useQueryClient();
  const saved = order.staff_note.text;
  const [typed, setTyped] = useState<string | null>(null);
  const [state, setState] = useState<"idle" | "saving" | "saved" | "cleared">("idle");
  const [failure, setFailure] = useState<unknown>(null);
  const value = typed ?? saved;
  const tooLong = [...value.trim()].length > STAFF_NOTE_MAX;

  if (!canManage && saved === "") return null;

  const by = order.staff_note.by !== "" && order.staff_note.at !== null && order.staff_note.at !== undefined
    ? t("shop.note.by", { name: order.staff_note.by, time: formatShopTime(order.staff_note.at, country, locale) })
    : null;

  const save = async () => {
    if (typed === null || !staffNoteChanged(typed, saved) || tooLong) return;
    setState("saving");
    setFailure(null);
    try {
      const fresh = await saveShopOrderNote(orgId, order.id, typed.trim());
      client.setQueryData(["shopOrder", orgId, order.id], fresh);
      void client.invalidateQueries({ queryKey: ["shopOrders", orgId] });
      // Keep showing the saved text until the card hands the fresh order down: no flash of the old one.
      setTyped(fresh.staff_note.text);
      setState(fresh.staff_note.text === "" ? "cleared" : "saved");
    } catch (error) {
      setState("idle");
      setFailure(error);
    }
  };

  return (
    <section aria-labelledby="shop-order-note-title" data-testid="shop-order-staff-note">
      <h2 id="shop-order-note-title">{t("shop.note.title")}</h2>
      {canManage ? (
        <TextAreaField
          label={t("shop.note.title")}
          hint={t("shop.note.hint")}
          error={tooLong ? t("shop.note.tooLong", { max: STAFF_NOTE_MAX }) : undefined}
          value={value}
          rows={3}
          onChange={(v) => {
            setTyped(v);
            setState("idle");
          }}
          onBlur={() => void save()}
          testId="shop-order-note-input"
        />
      ) : (
        <p data-testid="shop-order-note-text">{saved}</p>
      )}
      <p className="muted" role="status" aria-live="polite" data-testid="shop-order-note-state">
        {state === "saving" && t("shop.note.saving")}
        {state === "saved" && t("shop.note.saved")}
        {state === "cleared" && t("shop.note.cleared")}
        {state === "idle" && by !== null && <span data-testid="shop-order-note-by">{by}</span>}
      </p>
      {state === "saved" && by !== null && (
        <p className="muted" data-testid="shop-order-note-by">
          {by}
        </p>
      )}
      {failure !== null && <ErrorNotice error={failure} testId="shop-order-note-error" />}
    </section>
  );
}
