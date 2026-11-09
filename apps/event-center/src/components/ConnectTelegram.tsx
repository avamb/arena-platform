/**
 * F-007 (acceptance run 2026-11-09): "Connect Telegram". A person who works
 * in the web event center links their Telegram to the bot in THIS
 * organization: the server gives a one-time t.me link (15 minutes) and its
 * QR code, the bot asks "Connect this Telegram to <organization>?" and links
 * the account only on that press.
 *
 * The block shows nothing when the server has no bot (`available: false`) or
 * the caller may not ask (an API key, a 403). While a link is on screen the
 * status is read again every few seconds, so the page says "connected" as
 * soon as the person pressed the button in Telegram.
 */
import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { createTelegramConnectCode, getTelegramConnect } from "@/lib/api/endpoints";
import type { TelegramConnectCode } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { connectedSinceLink, connectExpired, connectTimeLeft, pngBlobOf } from "@/lib/telegramConnect";

const POLL_MS = 4000;

export function ConnectTelegram({ orgId, variant = "card" }: { orgId: string; variant?: "card" | "section" }) {
  const { t } = useI18n();
  const [link, setLink] = useState<TelegramConnectCode | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [used, setUsed] = useState(false);
  const waiting = link !== null && !used && !connectExpired(link.expires_at, now);
  const status = useQuery({
    queryKey: ["telegramConnect", orgId],
    queryFn: () => getTelegramConnect(orgId),
    retry: false,
    refetchInterval: waiting ? POLL_MS : false,
  });
  const issue = useMutation({
    mutationFn: () => createTelegramConnectCode(orgId),
    onSuccess: (code) => {
      setLink(code);
      setUsed(false);
      setNow(Date.now());
    },
  });

  // A clock for "works for N more minutes" and the expiry.
  useEffect(() => {
    if (link === null) return;
    const id = setInterval(() => setNow(Date.now()), 15_000);
    return () => clearInterval(id);
  }, [link]);

  // The QR picture: a blob address (img-src allows blob:), revoked with the link.
  const qrUrl = useMemo(() => {
    if (link === null || link.qr_png === null || link.qr_png === "") return null;
    const blob = pngBlobOf(link.qr_png);
    return blob === null ? null : URL.createObjectURL(blob);
  }, [link]);
  useEffect(() => () => {
    if (qrUrl !== null) URL.revokeObjectURL(qrUrl);
  }, [qrUrl]);

  const s = status.data;
  // The link on screen was used: the bot linked an account after it was made.
  const connectedNow = link !== null && s !== undefined && connectedSinceLink(s.accounts, link.expires_at);
  useEffect(() => {
    if (connectedNow) setUsed(true);
  }, [connectedNow]);
  if (s === undefined || !s.available) return null;
  const names = s.accounts.map((a) => (a.telegram_username === null ? null : `@${a.telegram_username}`)).filter((x): x is string => x !== null);

  const Wrapper = variant === "section" ? "section" : "div";
  return (
    <Wrapper className={variant === "card" ? "notice connect-telegram" : "org-section connect-telegram"} data-testid="connect-telegram" aria-labelledby="connect-telegram-title">
      <h2 id="connect-telegram-title">{t("tgConnect.title")}</h2>
      {s.linked ? (
        <p role="status" data-testid="connect-telegram-linked">
          {names.length > 0 ? t("tgConnect.linkedAs", { names: names.join(", ") }) : t("tgConnect.linked")}
        </p>
      ) : (
        <p>{t("tgConnect.intro")}</p>
      )}
      {connectedNow && (
        <p className="notice notice-ok" role="status" data-testid="connect-telegram-done">
          {t("tgConnect.done")}
        </p>
      )}

      {link === null || connectedNow ? (
        <button
          type="button"
          className={s.linked ? "btn" : "btn btn-primary"}
          onClick={() => issue.mutate()}
          disabled={issue.isPending}
          aria-busy={issue.isPending}
          data-testid="connect-telegram-start"
        >
          {s.linked ? t("tgConnect.again") : t("tgConnect.button")}
        </button>
      ) : connectExpired(link.expires_at, now) ? (
        <div role="status" data-testid="connect-telegram-expired">
          <p>{t("tgConnect.expired")}</p>
          <button type="button" className="btn btn-primary" onClick={() => issue.mutate()} disabled={issue.isPending} data-testid="connect-telegram-renew">
            {t("tgConnect.renew")}
          </button>
        </div>
      ) : (
        <div className="connect-telegram-link" data-testid="connect-telegram-link">
          <ol className="steps">
            <li>{t("tgConnect.step.phone")}</li>
            <li>{t("tgConnect.step.computer")}</li>
            <li>{t("tgConnect.step.confirm")}</li>
          </ol>
          <p>
            <a className="btn btn-primary" href={link.url} target="_blank" rel="noopener noreferrer" data-testid="connect-telegram-open">
              {t("tgConnect.open")}
            </a>
          </p>
          {qrUrl !== null && (
            <img className="qr-img" src={qrUrl} width={240} height={240} alt={t("tgConnect.qrAlt")} data-testid="connect-telegram-qr" />
          )}
          <p className="muted">
            {t("tgConnect.expires", { minutes: connectTimeLeft(link.expires_at, now) })}
          </p>
          <button type="button" className="btn btn-link" onClick={() => issue.mutate()} disabled={issue.isPending} data-testid="connect-telegram-renew">
            {t("tgConnect.renew")}
          </button>
        </div>
      )}
      {issue.isError && (
        <p className="notice notice-bad" role="alert" data-testid="connect-telegram-failed">
          {t("tgConnect.failed")}
        </p>
      )}
    </Wrapper>
  );
}
