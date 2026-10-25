/**
 * The QR code of a sales link (spec 43, QL-12): the picture on screen (a PNG
 * of the API shown through a blob address, revoked when the link changes)
 * and the two downloads, PNG 1024 for a post and SVG for the print shop.
 */
import { useEffect, useState } from "react";
import { downloadSalesLinkQr } from "@/lib/api/endpoints";
import type { SalesLink } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { DownloadButton } from "@/components/ui";

type Picture = { phase: "loading" } | { phase: "ok"; url: string } | { phase: "failed" };

const PREVIEW_PX = 512;

export function SalesLinkQr({ orgId, link }: { orgId: string; link: SalesLink }) {
  const { t } = useI18n();
  const [picture, setPicture] = useState<Picture>({ phase: "loading" });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let url: string | null = null;
    let gone = false;
    setPicture({ phase: "loading" });
    downloadSalesLinkQr(orgId, link.id, "png", link.code, PREVIEW_PX)
      .then((file) => {
        if (gone) return;
        url = URL.createObjectURL(file.blob);
        setPicture({ phase: "ok", url });
      })
      .catch(() => {
        if (!gone) setPicture({ phase: "failed" });
      });
    return () => {
      gone = true;
      if (url !== null) URL.revokeObjectURL(url);
    };
  }, [orgId, link.id, link.code, attempt]);

  return (
    <section className="qr-box" aria-labelledby="link-qr-title" data-testid="link-qr">
      <h2 id="link-qr-title">{t("links.qr.title")}</h2>
      {picture.phase === "loading" && (
        <p className="muted" role="status">
          {t("links.qr.loading")}
        </p>
      )}
      {picture.phase === "ok" && <img className="qr-img" src={picture.url} alt={t("links.qr.alt", { label: link.label })} width={PREVIEW_PX} height={PREVIEW_PX} data-testid="link-qr-img" />}
      {picture.phase === "failed" && (
        <div className="notice notice-bad" role="alert">
          <p>{t("links.qr.failed")}</p>
          <button type="button" className="btn" onClick={() => setAttempt((n) => n + 1)}>
            {t("common.retry")}
          </button>
        </div>
      )}
      <p className="muted">{t("links.qr.hint")}</p>
      <div className="actions">
        <DownloadButton label="links.qr.png" testId="link-qr-png" fetchFile={() => downloadSalesLinkQr(orgId, link.id, "png", link.code, 1024)} />
        <DownloadButton label="links.qr.svg" testId="link-qr-svg" fetchFile={() => downloadSalesLinkQr(orgId, link.id, "svg", link.code)} />
      </div>
    </section>
  );
}
