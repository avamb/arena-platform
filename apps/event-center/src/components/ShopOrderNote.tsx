/**
 * The internal note of a shop order (A-10): "called, deliver after 18:00". Only the shop's own people see it,
 * the buyer, the letters and the storefront never do. It is saved by the "Save note" button or by Ctrl+Enter
 * (SA-26), not by leaving the field: a closed tab used to lose a text nobody had saved. While the typed text is
 * not saved, leaving the page asks first (`UnsavedChangesGuard`). The line under the field says who wrote the saved
 * note and when (the author's name as it was when the note was saved).
 *
 * Two people can edit the note. The note carries the time it was saved (`staff_note.at`); the one the person started
 * from is remembered, and before the save the order is read again. When the saved note is no longer that one, the
 * save stops and shows the other person's text: replace it with mine, or keep theirs.
 *
 * A reader (no `shop.manage`) sees the note as text, and sees nothing when there is none.
 */
import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type KeyboardEvent } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextAreaField } from "@/components/FormField";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import { fetchShopOrder, saveShopOrderNote } from "@/lib/api/shopApi";
import type { ShopOrder } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { STAFF_NOTE_MAX, formatShopTime, staffNoteChanged } from "@/lib/shop";

type Note = ShopOrder["staff_note"];

export function ShopOrderNote({ orgId, order, canManage, country }: { orgId: string; order: ShopOrder; canManage: boolean; country?: string | null }) {
  const { t, locale } = useI18n();
  const client = useQueryClient();
  const saved = order.staff_note.text;
  const [typed, setTyped] = useState<string | null>(null);
  const [state, setState] = useState<"idle" | "saving" | "saved" | "cleared">("idle");
  const [failure, setFailure] = useState<unknown>(null);
  // The note the person started from: its save time is the version they are editing.
  const [baseAt, setBaseAt] = useState<string | null>(order.staff_note.at ?? null);
  // Another person's note that got in first, found when the person pressed Save.
  const [theirs, setTheirs] = useState<Note | null>(null);
  const allowLeave = useRef(false);
  const value = typed ?? saved;
  const tooLong = [...value.trim()].length > STAFF_NOTE_MAX;
  const dirty = canManage && typed !== null && staffNoteChanged(typed, saved);
  // The order reached the card with a note saved after the one the person started from (the list was refreshed).
  const behind: Note | null = canManage && dirty && (order.staff_note.at ?? null) !== baseAt ? order.staff_note : null;
  const other = theirs ?? behind;

  if (!canManage && saved === "") return null;

  const by =
    order.staff_note.by !== "" && order.staff_note.at !== null && order.staff_note.at !== undefined
      ? t("shop.note.by", { name: order.staff_note.by, time: formatShopTime(order.staff_note.at, country, locale) })
      : null;

  const write = async (text: string): Promise<boolean> => {
    setState("saving");
    setFailure(null);
    try {
      const fresh = await saveShopOrderNote(orgId, order.id, text.trim());
      client.setQueryData(["shopOrder", orgId, order.id], fresh);
      void client.invalidateQueries({ queryKey: ["shopOrders", orgId] });
      // Keep showing the saved text until the card hands the fresh order down: no flash of the old one.
      setTyped(fresh.staff_note.text);
      setBaseAt(fresh.staff_note.at ?? null);
      setTheirs(null);
      setState(fresh.staff_note.text === "" ? "cleared" : "saved");
      return true;
    } catch (error) {
      setState("idle");
      setFailure(error);
      return false;
    }
  };

  /** Saves the typed note; stops with the other person's text when a note was saved after the one this one started from. */
  const save = async (): Promise<boolean> => {
    if (typed === null || !staffNoteChanged(typed, saved) || tooLong || state === "saving") return true;
    setFailure(null);
    try {
      const latest = await fetchShopOrder(orgId, order.id);
      if ((latest.staff_note.at ?? null) !== baseAt) {
        client.setQueryData(["shopOrder", orgId, order.id], latest);
        setTheirs(latest.staff_note);
        return false;
      }
    } catch (error) {
      setFailure(error);
      return false;
    }
    return write(typed);
  };

  /** Drops the typed text and takes the other person's note. */
  const useTheirs = () => {
    const note = other;
    if (note === null) return;
    setTyped(null);
    setBaseAt(note.at ?? null);
    setTheirs(null);
    setState("idle");
  };

  /** Writes the typed note over the other person's. */
  const overwrite = () => {
    const note = other;
    if (typed === null) return;
    if (note !== null) setBaseAt(note.at ?? null);
    void write(typed);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      void save();
    }
  };

  return (
    <section aria-labelledby="shop-order-note-title" data-testid="shop-order-staff-note">
      <UnsavedChangesGuard dirty={dirty} allowLeave={allowLeave} onSaveAndLeave={canManage ? save : undefined} />
      <h2 id="shop-order-note-title">{t("shop.note.title")}</h2>
      {canManage ? (
        <div onKeyDown={onKeyDown}>
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
            testId="shop-order-note-input"
          />
          <p>
            <button type="button" className="btn" disabled={!dirty || tooLong || state === "saving"} onClick={() => void save()} data-testid="shop-order-note-save">
              {t("shop.note.save")}
            </button>
          </p>
        </div>
      ) : (
        <p data-testid="shop-order-note-text">{saved}</p>
      )}
      {other !== null && dirty && (
        <div className="notice notice-warn" role="alert" data-testid="shop-order-note-changed">
          <p>
            {t("shop.note.changedBy", { who: other.by !== "" ? other.by : t("shop.note.someone"), text: other.text === "" ? "—" : other.text })}
          </p>
          <p className="actions">
            <button type="button" className="btn" onClick={overwrite} data-testid="shop-order-note-overwrite">
              {t("shop.note.overwrite")}
            </button>
            <button type="button" className="btn" onClick={useTheirs} data-testid="shop-order-note-use-theirs">
              {t("shop.note.useTheirs")}
            </button>
          </p>
        </div>
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
