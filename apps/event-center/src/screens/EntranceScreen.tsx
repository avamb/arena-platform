/**
 * "Entrance to the event": one page for the door. It tells where to get the
 * ticket-scanner app (the same two store links as the bot's "Scanner"
 * screen) and where the guest list of a session is in the web. The guest
 * list itself is a file on the session page; the person's own automatic
 * delivery of it is an organization-level setting, shown here too.
 */
import { Link } from "@tanstack/react-router";
import { GuestListSubscription } from "@/components/GuestListSubscription";
import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

export const SCANNER_ANDROID_URL = "https://play.google.com/store/apps/details?id=com.arenasoldout.macs";
export const SCANNER_IOS_URL = "https://apps.apple.com/app/arenasoldout/id6770095694";

export function EntranceScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  return (
    <section aria-labelledby="entrance-title" data-testid="entrance">
      <h1 id="entrance-title">{t("entrance.title")}</h1>
      <p>{t("entrance.intro")}</p>

      <section className="card" id="scanner" aria-labelledby="entrance-scanner-title" data-testid="entrance-scanner">
        <h2 id="entrance-scanner-title">{t("entrance.scanner.title")}</h2>
        <p>{t("entrance.scanner.text")}</p>
        <p className="entrance-stores">
          <a className="btn btn-primary" href={SCANNER_ANDROID_URL} target="_blank" rel="noopener noreferrer" data-testid="entrance-android">
            {t("entrance.scanner.android")}
          </a>{" "}
          <a className="btn btn-primary" href={SCANNER_IOS_URL} target="_blank" rel="noopener noreferrer" data-testid="entrance-ios">
            {t("entrance.scanner.ios")}
          </a>
        </p>
      </section>

      <section className="card" id="guest-list" aria-labelledby="entrance-guest-title" data-testid="entrance-guest-list">
        <h2 id="entrance-guest-title">{t("entrance.guest.title")}</h2>
        {access.can("order.read") ? (
          <>
            <p>{t("entrance.guest.text")}</p>
            <p>
              <Link to="/o/$orgId/events" params={{ orgId }} className="btn" data-testid="entrance-events">
                {t("entrance.guest.open")}
              </Link>
            </p>
          </>
        ) : (
          <p className="muted" data-testid="entrance-guest-no-access">
            {t("entrance.guest.noAccess")}
          </p>
        )}
      </section>
      {access.can("order.read") && <GuestListSubscription orgId={orgId} />}
    </section>
  );
}
