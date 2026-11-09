/**
 * The "White label" tab of the price screen (spec 08_architecture/53): the
 * list of white label periods, a dialog to turn white label on for a brand
 * and a dialog to set the last day of a period. A period is never edited or
 * deleted: it is ended, and a new one is created.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError } from "@/lib/api/client";
import {
  buildWhiteLabelRequest,
  canEndWhiteLabel,
  createWhiteLabel,
  emptyWhiteLabelForm,
  endWhiteLabel,
  listWhiteLabel,
  monthlyPriceLabel,
  todayUTC,
  whiteLabelStatusLabel,
  type WhiteLabelForm,
  type WhiteLabelPeriod,
} from "@/lib/api/billingWhiteLabel";
import {
  Dialog,
  buttonStyle,
  disabledButtonStyle,
  errorStyle,
  inputStyle,
  labelStyle,
  noticeStyle,
  primaryButtonStyle,
} from "./onboardingUi";
import type { OrgChoice } from "./billingPricesRates";

type Modal = { kind: "none" } | { kind: "new"; form: WhiteLabelForm } | { kind: "end"; period: WhiteLabelPeriod; endsOn: string };

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : "The request did not complete. Please try again.";
}

export function WhiteLabelPanel({ orgs }: { orgs: readonly OrgChoice[] }): JSX.Element {
  const qc = useQueryClient();
  const [modal, setModal] = useState<Modal>({ kind: "none" });
  const [errors, setErrors] = useState<readonly string[]>([]);
  const [serverError, setServerError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const list = useQuery({ queryKey: ["admin", "billing", "white-label"], queryFn: listWhiteLabel, retry: false, refetchOnWindowFocus: false });

  const close = (): void => {
    setModal({ kind: "none" });
    setErrors([]);
    setServerError(null);
  };
  const done = (text: string): void => {
    close();
    setNotice(text);
    void qc.invalidateQueries({ queryKey: ["admin", "billing", "white-label"] });
  };
  const create = useMutation({
    mutationFn: createWhiteLabel,
    onSuccess: (p) => done(`White label is saved. It starts ${p.starts_on}.`),
    onError: (e) => setServerError(messageOf(e)),
  });
  const end = useMutation({
    mutationFn: (v: { id: string; endsOn: string }) => endWhiteLabel(v.id, v.endsOn),
    onSuccess: (p) => done(`The last day of white label is ${p.ends_on ?? ""}.`),
    onError: (e) => setServerError(messageOf(e)),
  });
  const busy = create.isPending || end.isPending;

  const submitNew = (form: WhiteLabelForm): void => {
    const built = buildWhiteLabelRequest(form);
    if (!built.ok) {
      setErrors(built.errors);
      return;
    }
    setErrors([]);
    setServerError(null);
    create.mutate(built.request);
  };
  const submitEnd = (period: WhiteLabelPeriod, endsOn: string): void => {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(endsOn)) {
      setErrors(["Last day: select a date."]);
      return;
    }
    setErrors([]);
    setServerError(null);
    end.mutate({ id: period.id, endsOn });
  };

  const items = list.data?.items ?? [];

  return (
    <div data-testid="billing-white-label">
      <p style={{ margin: "0 0 12px", color: "#475569", fontSize: 14 }}>
        White label hides the line "Tickets: Arena Sold Out" in the widget. It hides the platform line in the e-mails and
        the footer of the shop for one brand. The brand is the organization or one of its promoters. We charge each full
        month for the number of days. The days are UTC calendar days.
      </p>
      <div style={{ marginBottom: 12 }}>
        <button
          type="button"
          style={primaryButtonStyle}
          onClick={() => {
            setNotice(null);
            setModal({ kind: "new", form: emptyWhiteLabelForm() });
          }}
          data-testid="white-label-new"
        >
          Turn white label on
        </button>
      </div>
      {notice !== null ? (
        <div style={noticeStyle} role="status" data-testid="white-label-notice">
          {notice}
        </div>
      ) : null}
      {list.error ? (
        <div style={errorStyle} role="alert">
          {messageOf(list.error)}
        </div>
      ) : null}
      {items.length === 0 && !list.isPending ? <p>No brand has white label.</p> : null}
      {items.length > 0 ? (
        <div style={{ overflowX: "auto" }}>
          <table style={{ borderCollapse: "collapse", width: "100%", fontSize: 14 }} data-testid="white-label-table">
            <thead>
              <tr>
                {["Organization", "Brand", "First day", "Last day", "Monthly price", "State", "Cause", ""].map((h) => (
                  <th key={h} style={{ textAlign: "left", padding: "6px 8px", borderBottom: "1px solid #e2e8f0" }}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {items.map((p) => (
                <tr key={p.id} data-testid={`white-label-row-${p.id}`}>
                  <td style={{ padding: "6px 8px" }}>{p.org_name ?? p.org_id}</td>
                  <td style={{ padding: "6px 8px" }}>{p.promoter_id ? p.promoter_name ?? p.promoter_id : "The organization"}</td>
                  <td style={{ padding: "6px 8px" }}>{p.starts_on}</td>
                  <td style={{ padding: "6px 8px" }}>{p.ends_on ?? "No end"}</td>
                  <td style={{ padding: "6px 8px" }}>{monthlyPriceLabel(p.monthly_price_minor, p.currency)}</td>
                  <td style={{ padding: "6px 8px" }}>{whiteLabelStatusLabel(p.status)}</td>
                  <td style={{ padding: "6px 8px" }}>{p.reason}</td>
                  <td style={{ padding: "6px 8px" }}>
                    {canEndWhiteLabel(p) ? (
                      <button
                        type="button"
                        style={buttonStyle}
                        onClick={() => {
                          setNotice(null);
                          setModal({ kind: "end", period: p, endsOn: todayUTC() });
                        }}
                        data-testid={`white-label-end-${p.id}`}
                      >
                        Set the last day
                      </button>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      {modal.kind === "new" ? (
        <Dialog
          title="Turn white label on"
          testId="white-label-dialog"
          onClose={close}
          onSubmit={() => submitNew(modal.form)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="white-label-save">
                Save
              </button>
            </>
          }
        >
          <WhiteLabelFormBody form={modal.form} orgs={orgs} onChange={(form) => setModal({ kind: "new", form })} />
          <Problems errors={errors} serverError={serverError} />
        </Dialog>
      ) : null}

      {modal.kind === "end" ? (
        <Dialog
          title="Set the last day of white label"
          testId="white-label-end-dialog"
          onClose={close}
          onSubmit={() => submitEnd(modal.period, modal.endsOn)}
          footer={
            <>
              <button type="button" style={buttonStyle} onClick={close}>
                Cancel
              </button>
              <button type="submit" style={{ ...primaryButtonStyle, ...(busy ? disabledButtonStyle : {}) }} disabled={busy} data-testid="white-label-end-save">
                Save
              </button>
            </>
          }
        >
          <p style={{ marginTop: 0 }}>
            The badge comes back on the day after the last day. If you select yesterday, white label ends immediately. The
            days that we charged stay charged.
          </p>
          <label style={labelStyle}>
            Last day (UTC)
            <input
              type="date"
              style={inputStyle}
              value={modal.endsOn}
              onChange={(e) => setModal({ ...modal, endsOn: e.target.value })}
              data-testid="white-label-end-date"
            />
          </label>
          <Problems errors={errors} serverError={serverError} />
        </Dialog>
      ) : null}
    </div>
  );
}

function WhiteLabelFormBody({
  form,
  orgs,
  onChange,
}: {
  form: WhiteLabelForm;
  orgs: readonly OrgChoice[];
  onChange: (f: WhiteLabelForm) => void;
}): JSX.Element {
  return (
    <div style={{ display: "grid", gap: 10 }}>
      <label style={labelStyle}>
        Organization
        <select style={inputStyle} value={form.orgId} onChange={(e) => onChange({ ...form, orgId: e.target.value })} data-testid="white-label-org">
          <option value="">Select an organization</option>
          {orgs.map((o) => (
            <option key={o.id} value={o.id}>
              {o.name}
            </option>
          ))}
        </select>
      </label>
      <label style={labelStyle}>
        Promoter ID (keep it empty for the organization)
        <input style={inputStyle} value={form.promoterId} onChange={(e) => onChange({ ...form, promoterId: e.target.value })} data-testid="white-label-promoter" />
      </label>
      <label style={labelStyle}>
        First day (UTC)
        <input type="date" style={inputStyle} value={form.startsOn} onChange={(e) => onChange({ ...form, startsOn: e.target.value })} data-testid="white-label-starts" />
      </label>
      <label style={labelStyle}>
        Last day (UTC, keep it empty for no end)
        <input type="date" style={inputStyle} value={form.endsOn} onChange={(e) => onChange({ ...form, endsOn: e.target.value })} data-testid="white-label-ends" />
      </label>
      <label style={labelStyle}>
        Monthly price (keep it empty for the price list, 0 is free)
        <input
          inputMode="decimal"
          style={inputStyle}
          value={form.monthlyPrice}
          onChange={(e) => onChange({ ...form, monthlyPrice: e.target.value })}
          data-testid="white-label-price"
        />
      </label>
      <label style={labelStyle}>
        Currency of the price (for example EUR). It must be the billing currency of the organization.
        <input
          style={inputStyle}
          maxLength={3}
          value={form.currency}
          onChange={(e) => onChange({ ...form, currency: e.target.value })}
          data-testid="white-label-currency"
        />
      </label>
    </div>
  );
}

function Problems({ errors, serverError }: { errors: readonly string[]; serverError: string | null }): JSX.Element | null {
  if (errors.length === 0 && serverError === null) return null;
  return (
    <div style={errorStyle} role="alert">
      {errors.map((e) => (
        <div key={e}>{e}</div>
      ))}
      {serverError !== null ? <div>{serverError}</div> : null}
    </div>
  );
}
