/**
 * Issue invitations on chosen seats (spec 46, 4.1).
 *
 * The hall is the seat map of the seat manager in "invite" mode: a free seat
 * and a seat the organizer holds back (hatched) can be picked, a sold or held
 * one cannot. The panel beside it lists what is picked, takes general
 * admission places by category, the guest and how the tickets reach them.
 *
 * Nothing is reserved while the person thinks: the send is ONE request and the
 * server decides. If a seat was taken meanwhile (409 `invitation.seats_taken`,
 * or the background read of the hall notices it), the seat leaves the pick, the
 * map is read again, the taken seats are outlined and the form stays as typed.
 * Each send carries a request id that a retry of the SAME body reuses, so a
 * dropped answer can never issue two invitations.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { SeatMapCanvas, type MapTool, type SeatMapHandle } from "@/components/seatManager/SeatMapCanvas";
import { ToolBar } from "@/components/seatManager/SeatPanels";
import { InvitationActions } from "@/components/staffInvitations/InvitationActions";
import { SelectField, TextAreaField, TextField } from "@/components/FormField";
import { Loading } from "@/components/ui";
import { fetchSessionSchema, fetchSessionSeatsAdmin, issueStaffInvitation, listStaffInvitations, listTiers } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import type { StaffInvitationIssueResponse, TicketTierItem } from "@/lib/api/types";
import { formatMoney, formatNumber } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { orderTiers, tierColors } from "@/lib/seatManager/colors";
import { inviteLook } from "@/lib/seatManager/inviteLook";
import { buildHallIndex, seatsOfRow } from "@/lib/seatManager/model";
import {
  DELIVERIES,
  GUEST_LOCALES,
  MAX_GUEST_NAME,
  MAX_NOTE,
  MAX_PLACES,
  STAFF_INVITATIONS_KEY,
  buildIssueBody,
  chargeLine,
  dropUnpickable,
  emptyForm,
  formProblems,
  gaCounts,
  gaLines,
  gaTotal,
  groupSelection,
  isDefinitiveRefusal,
  isGaTier,
  invitationState,
  isSeatConflict,
  pickSeats,
  pollDelay,
  requestIdFor,
  seatIndex,
  takenSeatKeys,
  withRequestId,
  type FormProblem,
  type GuestForm,
  type PendingRequest,
  type SeatInfo,
} from "@/lib/staffInvitations";

interface Props {
  orgId: string;
  eventId: string;
  sessionId: string;
  /** The person may read the hall map (`session.seats.manage`). */
  canSeeHall: boolean;
  /** The person may read the list (`complimentary.read`): the result card then follows the tickets. */
  canRead: boolean;
  /** Called after an invitation was issued, so the list reads again. */
  onIssued?: (res: StaffInvitationIssueResponse) => void;
}

const PROBLEM_KEY: Record<FormProblem, MessageKey> = {
  noPlaces: "seatInv.problem.noPlaces",
  tooMany: "seatInv.problem.tooMany",
  name: "seatInv.problem.name",
  nameTooLong: "seatInv.problem.nameTooLong",
  email: "seatInv.problem.email",
  emailRequired: "seatInv.problem.emailRequired",
  noteTooLong: "seatInv.problem.noteTooLong",
};

export function SeatInvite({ orgId, eventId, sessionId, canSeeHall, canRead, onIssued }: Props) {
  const { t, locale } = useI18n();
  const qc = useQueryClient();
  const mapRef = useRef<SeatMapHandle>(null);

  const schemaQ = useQuery({
    queryKey: ["seat-schema", sessionId],
    queryFn: () => fetchSessionSchema(sessionId),
    staleTime: 5 * 60_000,
    retry: false,
    enabled: canSeeHall,
  });
  const seatsQ = useQuery({
    queryKey: ["seats-admin", sessionId],
    queryFn: () => fetchSessionSeatsAdmin(sessionId),
    refetchInterval: 8000,
    enabled: canSeeHall,
  });
  const tiersQ = useQuery({
    queryKey: ["tiers", orgId, eventId, sessionId],
    queryFn: () => listTiers(orgId, eventId, sessionId),
    enabled: canSeeHall,
  });

  const noMap = schemaQ.error instanceof ApiError && schemaQ.error.status === 404;
  const geometry = schemaQ.data?.geometry;
  const index = useMemo(() => (geometry !== undefined ? buildHallIndex(geometry) : null), [geometry]);
  const rows = useMemo(() => seatsQ.data?.seats ?? [], [seatsQ.data]);
  const seats = useMemo(() => seatIndex(rows), [rows]);

  const allTiers = useMemo(() => tiersQ.data?.tiers ?? [], [tiersQ.data]);
  const seatTiers = useMemo(() => allTiers.filter((x) => !isGaTier(x)), [allTiers]);
  const gaTiers = useMemo(() => orderTiers(allTiers.filter(isGaTier)), [allTiers]);
  const colors = useMemo(() => tierColors(seatTiers), [seatTiers]);
  const tierById = useMemo(() => new Map(allTiers.map((x) => [x.id, x] as const)), [allTiers]);

  // ── state ───────────────────────────────────────────────────────────────
  const [tool, setTool] = useState<MapTool>("seat");
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [conflict, setConflict] = useState<ReadonlySet<string>>(new Set());
  const [ga, setGa] = useState<Readonly<Record<string, number>>>({});
  const [form, setForm] = useState<GuestForm>(() => emptyForm(locale));
  const [tried, setTried] = useState(false);
  const [note, setNote] = useState<{ kind: "refused" | "limit"; count: number } | null>(null);
  const [result, setResult] = useState<StaffInvitationIssueResponse | null>(null);
  const pending = useRef<PendingRequest | null>(null);
  const selectedRef = useRef(selected);
  selectedRef.current = selected;

  const places = selected.size + gaTotal(ga);
  const problems = formProblems(form, places);
  const set = <K extends keyof GuestForm>(key: K, value: GuestForm[K]) => setForm((f) => ({ ...f, [key]: value }));

  // A seat that was picked and is no longer free (a buyer, a hold, a colleague): out of the pick, outlined.
  useEffect(() => {
    if (seats.size === 0) return;
    const kept = dropUnpickable(selectedRef.current, seats);
    if (kept.size === selectedRef.current.size) return;
    const gone = [...selectedRef.current].filter((k) => !kept.has(k));
    setSelected(kept);
    setConflict((c) => new Set([...c, ...gone]));
  }, [seats]);

  const tierColor = useCallback((s: SeatInfo | undefined) => (s?.tierId != null ? (colors.get(s.tierId) ?? "#d1d5db") : "#d1d5db"), [colors]);
  const look = useCallback(
    (key: string) => {
      const seat = seats.get(key);
      return inviteLook({ seat, tierColor: tierColor(seat), picked: selected.has(key), conflict: conflict.has(key) });
    },
    [seats, tierColor, selected, conflict],
  );

  const pick = useCallback(
    (keys: readonly string[]) => {
      const res = pickSeats(selected, keys, seats, MAX_PLACES - gaTotal(ga));
      setSelected(res.next);
      setConflict(new Set());
      setResult(null);
      if (res.overLimit) setNote({ kind: "limit", count: MAX_PLACES });
      else if (res.refused > 0) setNote({ kind: "refused", count: res.refused });
      else setNote(null);
    },
    [selected, seats, ga],
  );
  const onSeats = useCallback((keys: string[]) => pick(keys), [pick]);
  const onRowHandle = useCallback(
    (rowId: string) => {
      if (index !== null) pick(seatsOfRow(index, rowId));
    },
    [index, pick],
  );

  const setGaQty = (tierId: string, next: number, max: number) => {
    const room = MAX_PLACES - selected.size - (gaTotal(ga) - (ga[tierId] ?? 0));
    const qty = Math.max(0, Math.min(next, max, room));
    setGa((g) => ({ ...g, [tierId]: qty }));
    setResult(null);
  };

  // ── send ────────────────────────────────────────────────────────────────
  const issue = useMutation({
    mutationFn: () => {
      const body = buildIssueBody(form, selected, gaLines(ga));
      pending.current = requestIdFor(pending.current, body);
      return issueStaffInvitation(orgId, sessionId, withRequestId(body, pending.current.id));
    },
    onSuccess: (res) => {
      pending.current = null;
      setResult(res);
      setSelected(new Set());
      setConflict(new Set());
      setGa({});
      setNote(null);
      setTried(false);
      // The next guest: the person, the contact and the note are new, the way of sending and the language stay.
      setForm((f) => ({ ...emptyForm(f.locale), delivery: f.delivery }));
      void qc.invalidateQueries({ queryKey: ["seats-admin", sessionId] });
      void qc.invalidateQueries({ queryKey: [STAFF_INVITATIONS_KEY, orgId, sessionId] });
      void qc.invalidateQueries({ queryKey: ["tiers", orgId, eventId, sessionId] });
      void qc.invalidateQueries({ queryKey: ["sessionSummary", orgId, sessionId] });
      onIssued?.(res);
    },
    onError: (err) => {
      // A definite answer closes the attempt. A dropped connection or a 5xx keeps the id: the retry is the same request.
      if (isDefinitiveRefusal(err)) pending.current = null;
      if (isSeatConflict(err)) {
        const taken = takenSeatKeys(err);
        if (taken.length > 0) {
          setSelected((s) => new Set([...s].filter((k) => !taken.includes(k))));
          setConflict(new Set(taken));
        }
        void qc.invalidateQueries({ queryKey: ["seats-admin", sessionId] });
        void qc.invalidateQueries({ queryKey: ["tiers", orgId, eventId, sessionId] });
      }
    },
  });

  const submit = () => {
    setTried(true);
    if (problems.length > 0 || issue.isPending) return;
    issue.mutate();
  };

  // ── render ──────────────────────────────────────────────────────────────
  if (!canSeeHall) {
    return (
      <p className="notice notice-warn" role="status" data-testid="inv-no-hall-access">
        {t("seatInv.noMapAccess")}
      </p>
    );
  }
  if (seatsQ.isLoading || tiersQ.isLoading || schemaQ.isLoading) return <Loading />;
  const hardError = [seatsQ.error, tiersQ.error, noMap ? null : schemaQ.error].find((e) => e !== null && e !== undefined);
  if (hardError !== undefined) return <ErrorNotice error={hardError} />;

  const fieldProblem = (p: FormProblem) => (tried && problems.includes(p) ? t(PROBLEM_KEY[p], { max: p === "tooMany" ? MAX_PLACES : p === "nameTooLong" ? MAX_GUEST_NAME : MAX_NOTE }) : undefined);
  const groups = groupSelection(selected, seats);
  const fromReserve = groups.reduce((n, g) => n + g.fromReserve, 0);
  const seatText = (s: SeatInfo) => t("seatInv.seat", { sector: s.sector, row: s.row, seat: s.seat });
  const charge = result !== null ? chargeLine(result.charge) : null;
  const conflictText = [...conflict].map((k) => seats.get(k)).filter((s): s is SeatInfo => s !== undefined).map(seatText);
  const placeProblem = tried ? problems.find((p) => p === "noPlaces" || p === "tooMany") : undefined;

  return (
    <section className="inv" aria-labelledby="inv-issue-title" data-testid="inv-issue">
      <h2 id="inv-issue-title">{t("seatInv.map.title")}</h2>
      <div className="inv-layout">
        <div className="inv-hallcol">
          {noMap || index === null || geometry === undefined ? (
            <p className="notice" role="status" data-testid="inv-no-map">
              {t("seatInv.noMap")}
            </p>
          ) : (
            <>
              <ToolBar tool={tool} onTool={setTool} />
              <div className="sm-map inv-hall" data-testid="inv-hall">
                <SeatMapCanvas
                  ref={mapRef}
                  geometry={geometry}
                  index={index}
                  label={t("seatInv.mapLabel")}
                  look={look}
                  tool={tool}
                  stage={null}
                  onSeats={onSeats}
                  onRowHandle={onRowHandle}
                  onStage={() => undefined}
                />
                <div className="sm-zoom" role="group" aria-label={t("seats.zoom")}>
                  <button type="button" className="sm-zoom-btn" onClick={() => mapRef.current?.zoomBy(1.4)} aria-label={t("seats.zoomIn")}>
                    +
                  </button>
                  <button type="button" className="sm-zoom-btn" onClick={() => mapRef.current?.zoomBy(1 / 1.4)} aria-label={t("seats.zoomOut")}>
                    −
                  </button>
                  <button type="button" className="sm-zoom-btn" onClick={() => mapRef.current?.fit()} aria-label={t("seats.zoomFit")}>
                    ⤢
                  </button>
                </div>
                <ul className="sm-legend" aria-label={t("seats.legend")}>
                  <li><span className="sm-key inv-key-free" aria-hidden="true" />{t("seatInv.legend.free")}</li>
                  <li><span className="sm-key sm-key-closed" aria-hidden="true" />{t("seatInv.legend.reserve")}</li>
                  <li><span className="sm-key sm-key-changed" aria-hidden="true" />{t("seatInv.legend.picked")}</li>
                  <li><span className="sm-key sm-key-sold" aria-hidden="true" />{t("seatInv.legend.taken")}</li>
                </ul>
              </div>
            </>
          )}
        </div>

        <div className="inv-side">
          {conflict.size > 0 && (
            <div className="notice notice-bad" role="alert" data-testid="inv-conflict">
              <p>
                <strong>{t("seatInv.conflict.title")}</strong>
              </p>
              <p>{t("seatInv.conflict.body", { seats: conflictText.length > 0 ? conflictText.join(" / ") : String(conflict.size) })}</p>
            </div>
          )}
          {note !== null && (
            <p className="notice notice-warn" role="status" data-testid="inv-note">
              {note.kind === "limit"
                ? t("seatInv.limit", { max: MAX_PLACES })
                : t("seatInv.refused", { count: formatNumber(note.count, locale) })}
            </p>
          )}

          <section aria-labelledby="inv-picked-title" data-testid="inv-picked">
            <h3 id="inv-picked-title">{t("seatInv.picked.title", { count: formatNumber(selected.size, locale) })}</h3>
            {groups.length === 0 ? (
              <p className="muted">{noMap ? t("seatInv.noMap") : t("seatInv.picked.none")}</p>
            ) : (
              <>
                {groups.map((g) => {
                  const tier: TicketTierItem | undefined = g.tierId !== null ? tierById.get(g.tierId) : undefined;
                  const price = tier !== undefined ? (tier.current_price ?? tier.price_amount) : 0;
                  const currency = tier?.currency ?? "EUR";
                  return (
                    <div key={g.tierId ?? "none"} className="inv-group" data-testid="inv-picked-group">
                      <p className="muted">
                        {t("seatInv.picked.value", {
                          category: tier?.name ?? "—",
                          price: formatMoney(price, currency, locale),
                          free: formatMoney(0, currency, locale),
                        })}
                      </p>
                      <ul className="inv-seats">
                        {g.seats.map((s) => (
                          <li key={s.key} data-testid="inv-picked-seat" data-seat={s.key} data-origin={s.status}>
                            <span>
                              {seatText(s)}
                              {s.status === "unavailable" && <span className="muted"> · {t("seatInv.picked.reserve")}</span>}
                            </span>
                            <button
                              type="button"
                              className="btn inv-remove"
                              aria-label={t("seatInv.picked.remove", { seat: seatText(s) })}
                              onClick={() => pick([s.key])}
                            >
                              ×
                            </button>
                          </li>
                        ))}
                      </ul>
                    </div>
                  );
                })}
                {fromReserve > 0 && (
                  <p className="muted" data-testid="inv-from-reserve">
                    {t("seatInv.picked.fromReserve", { count: formatNumber(fromReserve, locale) })}
                  </p>
                )}
                <button type="button" className="btn" onClick={() => setSelected(new Set())} data-testid="inv-clear">
                  {t("seatInv.picked.clear")}
                </button>
              </>
            )}
          </section>

          {gaTiers.length > 0 && (
            <section aria-labelledby="inv-ga-title" data-testid="inv-ga">
              <h3 id="inv-ga-title">{t("seatInv.ga.title")}</h3>
              {gaTiers.map((x) => {
                const c = gaCounts(rows, x.id);
                const qty = ga[x.id] ?? 0;
                const max = c.free + c.reserve;
                return (
                  <div key={x.id} className="inv-ga-row" data-testid="inv-ga-row" data-tier={x.id}>
                    <span>{t("seatInv.ga.row", { category: x.name, free: formatNumber(c.free, locale), reserve: formatNumber(c.reserve, locale) })}</span>
                    <span className="inv-stepper" role="group" aria-label={t("seatInv.ga.qty", { category: x.name })}>
                      <button type="button" className="btn" disabled={qty === 0} aria-label={t("seatInv.ga.less", { category: x.name })} onClick={() => setGaQty(x.id, qty - 1, max)} data-testid="inv-ga-less">
                        −
                      </button>
                      <output aria-live="polite" data-testid="inv-ga-qty">{qty}</output>
                      <button type="button" className="btn" disabled={qty >= max || places >= MAX_PLACES} aria-label={t("seatInv.ga.more", { category: x.name })} onClick={() => setGaQty(x.id, qty + 1, max)} data-testid="inv-ga-more">
                        +
                      </button>
                    </span>
                  </div>
                );
              })}
            </section>
          )}

          <form
            className="inv-form"
            noValidate
            aria-labelledby="inv-guest-title"
            data-testid="inv-form"
            onSubmit={(e) => {
              e.preventDefault();
              submit();
            }}
          >
            <h3 id="inv-guest-title">{t("seatInv.guest.title")}</h3>
            <TextField label={t("seatInv.guest.name")} value={form.name} onChange={(v) => set("name", v)} testId="inv-name" autoComplete="off" error={fieldProblem("name") ?? fieldProblem("nameTooLong")} />
            <TextField label={t("seatInv.guest.email")} type="email" value={form.email} onChange={(v) => set("email", v)} testId="inv-email" error={fieldProblem("email") ?? fieldProblem("emailRequired")} />
            <TextField label={t("seatInv.guest.phone")} type="tel" value={form.phone} onChange={(v) => set("phone", v)} testId="inv-phone" />
            <SelectField label={t("seatInv.guest.locale")} value={form.locale} onChange={(v) => set("locale", v)} testId="inv-locale">
              {GUEST_LOCALES.map((l) => (
                <option key={l} value={l}>
                  {l.toUpperCase()}
                </option>
              ))}
            </SelectField>
            <TextAreaField label={t("seatInv.guest.note")} value={form.note} onChange={(v) => set("note", v)} testId="inv-note-field" rows={2} error={fieldProblem("noteTooLong")} />

            <fieldset className="inv-delivery" data-testid="inv-delivery">
              <legend>{t("seatInv.delivery.title")}</legend>
              {DELIVERIES.map((d) => (
                <label key={d} className="inv-radio">
                  <input type="radio" name="inv-delivery" value={d} checked={form.delivery === d} onChange={() => set("delivery", d)} data-testid={`inv-delivery-${d}`} />
                  <span>{t(`seatInv.delivery.${d}` as MessageKey)}</span>
                </label>
              ))}
            </fieldset>

            {placeProblem !== undefined && (
              <p className="field-error" role="alert" data-testid="inv-places-error">
                {t(PROBLEM_KEY[placeProblem], { max: MAX_PLACES })}
              </p>
            )}
            {issue.error !== null && takenSeatKeys(issue.error).length === 0 && <ErrorNotice error={issue.error} testId="inv-error" />}
            <button type="submit" className="btn btn-primary" disabled={issue.isPending} data-testid="inv-submit">
              {issue.isPending ? t("seatInv.submit.busy") : t("seatInv.submit", { count: formatNumber(places, locale) })}
            </button>
          </form>

          {result !== null && (
            <div className="notice notice-ok" role="status" data-testid="inv-result" data-order={result.order_id}>
              <p>
                <strong>{t("seatInv.result.title", { number: result.system_id })}</strong>
              </p>
              <p>{t("seatInv.result.body", { count: formatNumber(result.places.length, locale), guest: result.guest_name ?? "—" })}</p>
              {result.from_reserve > 0 && <p>{t("seatInv.result.fromReserve", { count: formatNumber(result.from_reserve, locale) })}</p>}
              {result.replayed && <p data-testid="inv-replayed">{t("seatInv.result.replayed")}</p>}
              {charge !== null && (
                <p data-testid="inv-charge">{t("seatInv.result.charge", { amount: formatMoney(charge.amountMinor, charge.currency, locale) })}</p>
              )}
              <ResultActions orgId={orgId} sessionId={sessionId} result={result} canRead={canRead} />
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

/** PDF and "send again" of the invitation just issued, enabled once its tickets exist (the list tells). */
function ResultActions({ orgId, sessionId, result, canRead }: { orgId: string; sessionId: string; result: StaffInvitationIssueResponse; canRead: boolean }) {
  const state = useQuery({
    queryKey: [STAFF_INVITATIONS_KEY, orgId, sessionId],
    queryFn: () => listStaffInvitations(orgId, sessionId),
    refetchInterval: (q) => pollDelay(q.state.data?.items ?? []),
    enabled: canRead,
    select: (d) => d.items.find((i) => i.order_id === result.order_id),
  });
  const { t } = useI18n();
  const item = state.data;
  const ready = item === undefined ? !canRead : invitationState(item) !== "issuing";
  return (
    <>
      {!ready && <p data-testid="inv-issuing">{t("seatInv.result.issuing")}</p>}
      <InvitationActions orgId={orgId} orderId={result.order_id} systemId={result.system_id} ready={ready} canResend={result.delivery !== "pdf_only"} />
    </>
  );
}
