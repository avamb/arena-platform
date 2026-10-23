/**
 * The card of ONE category of a date (task 5-4): open and close, price, sale
 * dates, quantity. The web twin of the bot's categories dialog
 * (eventbot/categories.go). The rules live in lib/categories.ts; this file
 * only draws them and calls the API.
 *
 * Every change is a request, never a promise: the card is drawn again from the
 * list the server answers after the write (the list is invalidated), so what
 * is on the screen is what is stored. A change reaches NEW purchases only.
 */
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@/lib/api/http";
import { fetchTierSchedule, putTierSchedule, updateTier, type TierRef } from "@/lib/api/endpoints";
import type { TicketTierItem } from "@/lib/api/types";
import {
  categoryCaps,
  categoryPriceNow,
  chainTargetName,
  edgeLabel,
  isCategoryOpen,
  parseCategoryPrice,
  parseCategoryQuantity,
  planPriceWrite,
  priceInputValue,
  quantityFloor,
  windowBounds,
  windowEdgeInstant,
} from "@/lib/categories";
import { formatDateTime, formatMoney, formatNumber } from "@/lib/format";
import { useI18n, type Params } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { DateField } from "@/components/DateField";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextField } from "@/components/FormField";
import { Badge } from "@/components/ui";

interface Notice {
  key: MessageKey;
  params?: Params;
}

type Mode = "none" | "price" | "priceConfirm" | "qty" | "windowStart" | "windowEnd";

type Action =
  | { kind: "toggle"; open: boolean }
  | { kind: "price"; amount: number }
  | { kind: "qty"; value: number }
  | { kind: "window"; edge: "start" | "end"; day: string | null };

export interface CategoryCardProps {
  orgId: string;
  eventId: string;
  sessionId: string;
  tierId: string;
  /** The fresh list of the date's categories. */
  tiers: readonly TicketTierItem[];
  /** The venue's zone. The sale dates are the venue's days, so they are changed only when it is known. */
  timezone: string | null;
  /** The moment the whole date stops selling (RFC 3339). It caps every window. */
  salesEndAt: string | null;
  onClose: () => void;
}

export function CategoryCard({ orgId, eventId, sessionId, tierId, tiers, timezone, salesEndAt, onClose }: CategoryCardProps) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const tier = tiers.find((x) => x.id === tierId);
  const ref: TierRef = { orgId, eventId, sessionId, tierId };

  const [mode, setMode] = useState<Mode>("none");
  const [notice, setNotice] = useState<Notice | null>(null);
  const [priceText, setPriceText] = useState("");
  const [priceError, setPriceError] = useState<MessageKey | null>(null);
  const [pendingPrice, setPendingPrice] = useState(0);
  const [qtyText, setQtyText] = useState("");
  const [qtyError, setQtyError] = useState<MessageKey | null>(null);
  const [pickedDay, setPickedDay] = useState<string | null>(null);

  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    heading.current?.focus();
  }, [tierId]);

  const caps = tier !== undefined ? categoryCaps(tier, tiers) : null;

  // The schedule is read to say, before the press, WHICH price will change.
  const followsSchedule = caps?.price === true && (mode === "price" || mode === "priceConfirm");
  const schedule = useQuery({
    queryKey: ["tierSchedule", orgId, eventId, sessionId, tierId],
    queryFn: () => fetchTierSchedule(ref),
    enabled: followsSchedule,
  });

  const write = useMutation({
    mutationFn: async (a: Action): Promise<Notice> => {
      if (tier === undefined) throw new Error("the category is gone");
      switch (a.kind) {
        case "toggle":
          await updateTier(ref, { is_open: a.open });
          return { key: a.open ? "categories.toggle.openedDone" : "categories.toggle.closedDone" };
        case "qty":
          await updateTier(ref, { capacity: a.value });
          return { key: "categories.qty.saved", params: { total: formatNumber(a.value, locale) } };
        case "window": {
          const value = a.day === null ? null : windowEdgeInstant(a.day, timezone, a.edge === "end");
          await updateTier(ref, a.edge === "start" ? { sale_window_start: value } : { sale_window_end: value });
          return { key: a.day === null ? "categories.window.removed" : "categories.window.saved" };
        }
        case "price": {
          // Read the schedule afresh: it may have moved since the question was asked.
          const sched = await fetchTierSchedule(ref);
          const plan = planPriceWrite(sched.price_schedule.windows, new Date(), a.amount);
          if (plan.kind === "window") await putTierSchedule(ref, plan.windows);
          else await updateTier(ref, { price_amount: a.amount });
          return {
            key: plan.kind === "window" ? "categories.price.savedWindow" : "categories.price.saved",
            params: { price: formatMoney(a.amount, tier.currency, locale) },
          };
        }
      }
    },
    onSuccess: (done) => {
      setNotice(done);
      setMode("none");
      setPickedDay(null);
      void queryClient.invalidateQueries({ queryKey: ["tiers", orgId, eventId, sessionId] });
      void queryClient.invalidateQueries({ queryKey: ["tierSchedule", orgId, eventId, sessionId, tierId] });
      void queryClient.invalidateQueries({ queryKey: ["sessionSummary", orgId, sessionId] });
    },
    onError: () => {
      // The list may have moved under us (a sale, another editor): read it again.
      void queryClient.invalidateQueries({ queryKey: ["tiers", orgId, eventId, sessionId] });
    },
  });

  if (tier === undefined || caps === null) {
    return (
      <section aria-labelledby="category-title" data-testid="category-card-gone">
        <h3 id="category-title">{t("categories.manage")}</h3>
        <p className="notice" role="status">
          {t("categories.gone")}
        </p>
        <button type="button" className="btn" onClick={onClose}>
          {t("categories.panel.close")}
        </button>
      </section>
    );
  }

  const open = isCategoryOpen(tier);
  const floor = quantityFloor(tier);
  const go = (next: Mode) => {
    write.reset();
    setNotice(null);
    setPriceError(null);
    setQtyError(null);
    setPickedDay(null);
    setMode(next);
  };
  const run = (a: Action) => {
    setNotice(null);
    write.mutate(a);
  };

  const edgeText = (instant: string | null | undefined, isEnd: boolean): string | null => {
    if (instant === null || instant === undefined) return null;
    return edgeLabel(instant, timezone, isEnd) ?? formatDateTime(instant, timezone, locale);
  };
  const first = edgeText(tier.sale_window_start, false);
  const last = edgeText(tier.sale_window_end, true);
  const windowText =
    first !== null && last !== null
      ? t("categories.window.both", { from: first, to: last })
      : first !== null
        ? t("categories.window.from", { date: first })
        : last !== null
          ? t("categories.window.until", { date: last })
          : t("categories.window.none");

  const onPriceCheck = (e: FormEvent) => {
    e.preventDefault();
    const p = parseCategoryPrice(priceText, tier.currency);
    if (!p.ok) {
      setPriceError(p.error);
      return;
    }
    setPriceError(null);
    setPendingPrice(p.value);
    setMode("priceConfirm");
  };
  const onQtySave = (e: FormEvent) => {
    e.preventDefault();
    const p = parseCategoryQuantity(qtyText);
    if (!p.ok) {
      setQtyError(p.error);
      return;
    }
    setQtyError(null);
    run({ kind: "qty", value: p.value });
  };

  // A refusal below what is sold or held has its own sentence with the real floor.
  const err = write.error;
  const belowUsed = err instanceof ApiError && err.code === "tier.quantity_below_used";
  const detailFloor = err instanceof ApiError && typeof err.details?.floor === "number" ? err.details.floor : floor;

  const priceRule =
    mode === "priceConfirm" && schedule.data !== undefined
      ? planPriceWrite(schedule.data.price_schedule.windows, new Date(), pendingPrice).kind
      : "base";

  const windowEditor = (edge: "start" | "end") => {
    const bounds = windowBounds(edge, tier, timezone, salesEndAt);
    return (
      <div data-testid={`window-editor-${edge}`}>
        <DateField
          value={pickedDay}
          onChange={setPickedDay}
          timezone={timezone ?? undefined}
          min={bounds.min}
          max={bounds.max}
          label={t(edge === "start" ? "categories.window.first" : "categories.window.last")}
          disabled={write.isPending}
        />
        <p className="field-hint">{t(edge === "start" ? "categories.window.firstHint" : "categories.window.lastHint")}</p>
        <div className="actions">
          <button
            type="button"
            className="btn btn-primary"
            disabled={pickedDay === null || write.isPending}
            onClick={() => pickedDay !== null && run({ kind: "window", edge, day: pickedDay })}
            data-testid="window-save"
          >
            {t("categories.window.save")}
          </button>
          <button type="button" className="btn" disabled={write.isPending} onClick={() => go("none")} data-testid="window-cancel">
            {t("categories.cancel")}
          </button>
        </div>
      </div>
    );
  };

  const edgeRow = (edge: "start" | "end", text: string | null) => {
    const stored = edge === "start" ? tier.sale_window_start : tier.sale_window_end;
    return (
      <li data-testid={`window-${edge}`}>
        <strong>{t(edge === "start" ? "categories.window.first" : "categories.window.last")}</strong>
        {": "}
        <span data-testid={`window-${edge}-value`}>{text ?? t("categories.window.none")}</span>
        <span className="actions">
          <button
            type="button"
            className="btn"
            disabled={write.isPending}
            onClick={() => go(edge === "start" ? "windowStart" : "windowEnd")}
            data-testid={`window-edit-${edge}`}
          >
            {t("categories.window.edit")}
          </button>
          {stored !== null && stored !== undefined && (
            <button
              type="button"
              className="btn"
              disabled={write.isPending}
              onClick={() => run({ kind: "window", edge, day: null })}
              data-testid={`window-remove-${edge}`}
            >
              {t("categories.window.remove")}
            </button>
          )}
        </span>
      </li>
    );
  };

  return (
    <section aria-labelledby="category-title" className="category-card" data-testid="category-card" data-tier-id={tier.id}>
      <h3 id="category-title" ref={heading} tabIndex={-1} data-testid="category-title">
        {t("categories.panel.title", { name: tier.name })}
      </h3>
      <ul className="facts">
        <li data-testid="category-state">
          {open ? <Badge tone="ok">{t("categories.state.open")}</Badge> : <Badge tone="neutral">{t("categories.state.closed")}</Badge>}
        </li>
        {!caps.notFixed && (
          <li data-testid="category-price">{t("categories.priceNow", { price: formatMoney(categoryPriceNow(tier), tier.currency, locale) })}</li>
        )}
        {!caps.notFixed && tier.next_price_change_at !== null && tier.next_price_change_at !== undefined && (
          <li>{t("categories.priceNext", { when: formatDateTime(tier.next_price_change_at, timezone, locale) })}</li>
        )}
        <li data-testid="category-places">
          {tier.quantity === null || tier.quantity === undefined
            ? t("categories.placesNone")
            : t("categories.places", {
                sold: formatNumber(tier.sold ?? 0, locale),
                held: formatNumber(tier.held ?? 0, locale),
                free: formatNumber(tier.available ?? 0, locale),
                total: formatNumber(tier.quantity, locale),
              })}
        </li>
        <li data-testid="category-window">{t("categories.window.label", { value: windowText })}</li>
        {salesEndAt !== null && <li>{t("categories.sessionEnd", { when: formatDateTime(salesEndAt, timezone, locale) })}</li>}
      </ul>

      {caps.notFixed && <p className="notice">{t(tier.pricing_mode === "pwyw" ? "categories.note.pwyw" : "categories.note.free")}</p>}
      {caps.chain && (
        <p className="notice" data-testid="category-note-chain">
          {chainTargetName(tier, tiers) !== ""
            ? t("categories.note.chain", { name: chainTargetName(tier, tiers) })
            : t("categories.note.chainTarget")}
        </p>
      )}
      {caps.seated && <p className="notice">{t("categories.note.seated")}</p>}

      {notice !== null && (
        <p className="notice notice-ok" role="status" data-testid="category-notice">
          {t(notice.key, notice.params)}
        </p>
      )}
      {err !== null && !belowUsed && <ErrorNotice error={err} testId="category-error" />}
      {belowUsed && (
        <p className="notice notice-bad" role="alert" data-testid="category-error">
          {t("categories.qty.belowUsed", { floor: formatNumber(detailFloor, locale) })}
        </p>
      )}

      {/* Open and close: one press, only NEW sales stop. */}
      {caps.toggle && (
        <div data-testid="category-toggle">
          <div className="actions">
            <button
              type="button"
              className="btn"
              disabled={write.isPending}
              onClick={() => run({ kind: "toggle", open: !open })}
              data-testid="toggle-btn"
            >
              {t(open ? "categories.toggle.close" : "categories.toggle.open")}
            </button>
          </div>
          {open && <p className="field-hint">{t("categories.toggle.closeHint")}</p>}
        </div>
      )}

      {/* Price. */}
      {caps.price && (
        <div data-testid="category-price-block">
          {mode !== "price" && mode !== "priceConfirm" && (
            <div className="actions">
              <button
                type="button"
                className="btn"
                disabled={write.isPending}
                onClick={() => {
                  go("price");
                  setPriceText(priceInputValue(categoryPriceNow(tier), tier.currency));
                }}
                data-testid="price-change"
              >
                {t("categories.price.change")}
              </button>
            </div>
          )}
          {mode === "price" && (
            <form onSubmit={onPriceCheck} noValidate data-testid="price-form">
              <TextField
                label={t("categories.price.label", { currency: tier.currency })}
                hint={t("categories.price.hint")}
                error={priceError !== null ? t(priceError) : undefined}
                value={priceText}
                onChange={setPriceText}
                testId="price-input"
              />
              <div className="actions">
                <button type="submit" className="btn btn-primary" data-testid="price-check">
                  {t("categories.price.check")}
                </button>
                <button type="button" className="btn" onClick={() => go("none")} data-testid="price-cancel">
                  {t("categories.cancel")}
                </button>
              </div>
            </form>
          )}
          {mode === "priceConfirm" && (
            <div role="group" aria-labelledby="price-confirm-title" data-testid="price-confirm">
              <p id="price-confirm-title">
                <strong>
                  {t("categories.price.confirm", {
                    name: tier.name,
                    old: formatMoney(categoryPriceNow(tier), tier.currency, locale),
                    new: formatMoney(pendingPrice, tier.currency, locale),
                  })}
                </strong>
              </p>
              <p>{t("categories.price.confirmNext")}</p>
              {priceRule === "window" && <p data-testid="price-confirm-window">{t("categories.price.confirmWindow")}</p>}
              <div className="actions">
                <button
                  type="button"
                  className="btn btn-primary"
                  disabled={write.isPending}
                  onClick={() => run({ kind: "price", amount: pendingPrice })}
                  data-testid="price-save"
                >
                  {t("categories.price.save")}
                </button>
                <button type="button" className="btn" disabled={write.isPending} onClick={() => go("price")} data-testid="price-back">
                  {t("categories.cancel")}
                </button>
              </div>
            </div>
          )}
        </div>
      )}

      {/* Sale dates: the venue's days. */}
      {caps.window && (
        <div data-testid="category-window-block">
          {timezone === null ? (
            <p className="notice" data-testid="window-no-zone">
              {t("categories.window.noZone")}
            </p>
          ) : mode === "windowStart" ? (
            windowEditor("start")
          ) : mode === "windowEnd" ? (
            windowEditor("end")
          ) : (
            <ul className="facts">
              {edgeRow("start", first)}
              {edgeRow("end", last)}
            </ul>
          )}
        </div>
      )}

      {/* Quantity. */}
      {caps.qty && (
        <div data-testid="category-qty-block">
          {mode !== "qty" ? (
            <div className="actions">
              <button
                type="button"
                className="btn"
                disabled={write.isPending}
                onClick={() => {
                  go("qty");
                  setQtyText(String(tier.quantity ?? ""));
                }}
                data-testid="qty-change"
              >
                {t("categories.qty.change")}
              </button>
            </div>
          ) : (
            <form onSubmit={onQtySave} noValidate data-testid="qty-form">
              <TextField
                label={t("categories.qty.label")}
                hint={t("categories.qty.hint", { floor: formatNumber(floor, locale) })}
                error={qtyError !== null ? t(qtyError) : undefined}
                value={qtyText}
                onChange={setQtyText}
                testId="qty-input"
              />
              <div className="actions">
                <button type="submit" className="btn btn-primary" disabled={write.isPending} data-testid="qty-save">
                  {t("categories.qty.save")}
                </button>
                <button type="button" className="btn" disabled={write.isPending} onClick={() => go("none")} data-testid="qty-cancel">
                  {t("categories.cancel")}
                </button>
              </div>
            </form>
          )}
        </div>
      )}

      <div className="actions">
        <button type="button" className="btn" onClick={onClose} data-testid="category-close">
          {t("categories.panel.close")}
        </button>
      </div>
    </section>
  );
}
