/**
 * Analytics: one person's timeline (spec 50, sections 9 and 10, stage A8).
 * The page behind the `arena_timeline_url` of a CRM card.
 *
 *   GET /v1/admin/analytics/people/{person_id}/timeline?limit=&cursor=
 *
 * The first page carries the profile (stage, touches, milestones, the
 * activity index and the line that explains it) and the newest sessions;
 * events come newest first, older ones on "Show older". No e-mail or name is
 * ever shown: the API holds none. Every time is shown in Europe/Madrid with
 * the zone named. Superadmin only (superadmin.read). Mock data: NONE.
 */
import { createRoute, Link, useParams } from "@tanstack/react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import type { CSSProperties } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError } from "@/lib/api/client";
import {
  fetchTimeline,
  formatMadrid,
  formatSeconds,
  gmvRows,
  milestoneRows,
  propsText,
  touchText,
  type PersonTimeline,
} from "@/lib/api/analytics";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { useTranslation } from "@/lib/i18n/I18nContext";
import {
  buttonStyle,
  disabledButtonStyle,
  errorStyle,
  headerRowStyle,
  headingStyle,
  hintStyle,
  mutedStyle,
  pageStyle,
  sectionStyle,
  sectionTitleStyle,
  subheadingStyle,
  tableStyle,
  tdStyle,
  thStyle,
} from "./onboardingUi";
import type { Translate } from "./analyticsUi";

const PERSON_NAV_ENTRY = NAV_BY_PATH["/analytics/flows"]!;

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/analytics/people/$personId",
  component: AnalyticsPersonRoute,
});

function AnalyticsPersonRoute() {
  const { personId } = useParams({ from: "/analytics/people/$personId" });
  return (
    <RequirePermission entry={PERSON_NAV_ENTRY}>
      <AnalyticsPersonPage personId={personId} />
    </RequirePermission>
  );
}

function AnalyticsPersonPage({ personId }: { personId: string }): JSX.Element {
  const { t } = useTranslation();
  const timeline = useInfiniteQuery<PersonTimeline, ApiError, { pages: PersonTimeline[] }, unknown[], string | null>({
    queryKey: ["analytics-timeline", personId],
    initialPageParam: null,
    queryFn: ({ pageParam }) => fetchTimeline(personId, pageParam),
    getNextPageParam: (last) => last.next_cursor,
    refetchOnWindowFocus: false,
    retry: false,
  });

  return (
    <div style={pageStyle} data-testid="analytics-person">
      <div style={{ marginBottom: 8, fontSize: 13 }}>
        <Link to="/analytics/flows" data-testid="analytics-person-back">
          {t("analytics.person.back")}
        </Link>
      </div>
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle}>{t("analytics.person.title")}</h1>
          <p style={subheadingStyle}>{personId}</p>
        </div>
      </div>
      {timeline.isLoading ? <p style={mutedStyle}>{t("analytics.loading")}</p> : null}
      {timeline.error ? (
        <div style={errorStyle} data-testid="analytics-error">
          {timeline.error.status === 404
            ? t("analytics.person.notFound")
            : t("analytics.loadError", { message: timeline.error.message })}
        </div>
      ) : null}
      {timeline.data ? (
        <TimelineView
          pages={timeline.data.pages}
          hasMore={timeline.hasNextPage === true}
          loadingMore={timeline.isFetchingNextPage}
          onLoadMore={() => void timeline.fetchNextPage()}
          t={t}
        />
      ) : null}
    </div>
  );
}

const factsStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))",
  gap: "10px 24px",
  margin: 0,
};
const dtStyle: CSSProperties = { fontSize: 11, fontWeight: 600, color: "#475569", textTransform: "uppercase", letterSpacing: 0.4 };
const ddStyle: CSSProperties = { margin: "2px 0 0 0", fontSize: 13, wordBreak: "break-word" };

function Fact({ label, children, testId }: { label: string; children: React.ReactNode; testId?: string }): JSX.Element {
  return (
    <div data-testid={testId}>
      <dt style={dtStyle}>{label}</dt>
      <dd style={ddStyle}>{children}</dd>
    </div>
  );
}

export interface TimelineViewProps {
  /** The pages fetched so far, newest first; the first one carries the profile. */
  readonly pages: readonly PersonTimeline[];
  readonly hasMore: boolean;
  readonly loadingMore: boolean;
  readonly onLoadMore: () => void;
  readonly t: Translate;
}

export function TimelineView({ pages, hasMore, loadingMore, onLoadMore, t }: TimelineViewProps): JSX.Element {
  const first = pages[0];
  if (!first) return <></>;
  const events = pages.flatMap((p) => p.events);
  const profile = first.profile;
  const milestones = profile ? milestoneRows(profile.milestones) : [];
  const gmv = profile ? gmvRows(profile.milestones) : [];

  return (
    <div data-testid="analytics-timeline">
      <p style={hintStyle}>{t("analytics.zoneNote")}</p>

      <section style={sectionStyle}>
        <h2 style={sectionTitleStyle}>{t("analytics.person.ids")}</h2>
        <dl style={factsStyle}>
          <Fact label={t("analytics.person.crm")} testId="analytics-person-crm">
            {first.twenty_person_id ?? "—"}
          </Fact>
          <Fact label={t("analytics.person.user")} testId="analytics-person-user">
            {first.user_id ?? "—"}
          </Fact>
          <Fact label={t("analytics.person.created")}>{formatMadrid(first.created_at)}</Fact>
          <Fact label={t("analytics.person.firstTouch")} testId="analytics-person-first-touch">
            {touchText(first.first_touch)}
          </Fact>
          <Fact label={t("analytics.person.lastTouch")} testId="analytics-person-last-touch">
            {touchText(first.last_touch)}
          </Fact>
        </dl>
      </section>

      <section style={sectionStyle}>
        <h2 style={sectionTitleStyle}>{t("analytics.person.profile")}</h2>
        {profile === null ? (
          <p style={mutedStyle} data-testid="analytics-no-profile">
            {t("analytics.person.noProfile")}
          </p>
        ) : (
          <>
            <dl style={factsStyle}>
              <Fact label={t("analytics.person.stage")} testId="analytics-person-stage">
                {profile.stage}
              </Fact>
              <Fact label={t("analytics.person.lastSeen")}>{formatMadrid(profile.last_seen_at)}</Fact>
              <Fact label={t("analytics.person.visits")}>{profile.visits_30d}</Fact>
              <Fact label={t("analytics.person.minutes")}>{profile.engaged_minutes_30d}</Fact>
              <Fact label={t("analytics.person.score")} testId="analytics-person-score">
                {profile.activity_score}
              </Fact>
            </dl>
            <p style={{ ...hintStyle, marginTop: 10 }} data-testid="analytics-person-why">
              <strong>{t("analytics.person.scoreWhy")}:</strong> {profile.score_explanation}
            </p>
            <h3 style={{ ...sectionTitleStyle, fontSize: 13, marginTop: 12 }}>{t("analytics.person.milestones")}</h3>
            {milestones.length === 0 && gmv.length === 0 ? (
              <p style={mutedStyle}>{t("analytics.person.noMilestones")}</p>
            ) : (
              <ul style={{ margin: 0, paddingLeft: 18, fontSize: 13 }} data-testid="analytics-person-milestones">
                {milestones.map((m) => (
                  <li key={m.name}>
                    <code>{m.name}</code> — {formatMadrid(m.at)}
                  </li>
                ))}
                {gmv.map((g) => (
                  <li key={g.currency}>
                    <code>gmv_30d</code> — {g.currency} {g.minor}
                  </li>
                ))}
              </ul>
            )}
            {profile.articles.length > 0 ? (
              <>
                <h3 style={{ ...sectionTitleStyle, fontSize: 13, marginTop: 12 }}>{t("analytics.person.articles")}</h3>
                <ul style={{ margin: 0, paddingLeft: 18, fontSize: 13 }} data-testid="analytics-person-articles">
                  {profile.articles.map((a, i) => (
                    <li key={`${String(a["path"])}|${i}`}>
                      {String(a["path"])} — {formatMadrid(typeof a["at"] === "string" ? a["at"] : null)}
                    </li>
                  ))}
                </ul>
              </>
            ) : null}
          </>
        )}
      </section>

      <section style={sectionStyle}>
        <h2 style={sectionTitleStyle}>{t("analytics.person.sessions")}</h2>
        {first.sessions.length === 0 ? (
          <p style={mutedStyle}>{t("analytics.person.noSessions")}</p>
        ) : (
          <div style={{ overflowX: "auto" }}>
            <table style={tableStyle} data-testid="analytics-person-sessions">
              <thead>
                <tr>
                  <th style={thStyle}>{t("analytics.person.col.time")}</th>
                  <th style={thStyle}>{t("analytics.col.surface")}</th>
                  <th style={thStyle}>{t("analytics.person.col.ended")}</th>
                  <th style={{ ...thStyle, textAlign: "right" }}>{t("analytics.person.col.pages")}</th>
                  <th style={{ ...thStyle, textAlign: "right" }}>{t("analytics.person.col.engaged")}</th>
                  <th style={thStyle}>{t("analytics.person.col.entry")}</th>
                </tr>
              </thead>
              <tbody>
                {first.sessions.map((s) => (
                  <tr key={s.id}>
                    <td style={tdStyle}>{formatMadrid(s.started_at)}</td>
                    <td style={tdStyle}>{s.surface}</td>
                    <td style={tdStyle}>{formatMadrid(s.ended_at)}</td>
                    <td style={{ ...tdStyle, textAlign: "right" }}>{s.pages}</td>
                    <td style={{ ...tdStyle, textAlign: "right" }}>{formatSeconds(s.engaged_seconds)}</td>
                    <td style={tdStyle}>{propsText(s.entry)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section style={sectionStyle}>
        <h2 style={sectionTitleStyle}>{t("analytics.person.events")}</h2>
        {events.length === 0 ? (
          <p style={mutedStyle}>{t("analytics.person.noEvents")}</p>
        ) : (
          <div style={{ overflowX: "auto" }}>
            <table style={tableStyle} data-testid="analytics-person-events">
              <thead>
                <tr>
                  <th style={thStyle}>{t("analytics.person.col.time")}</th>
                  <th style={thStyle}>{t("analytics.col.surface")}</th>
                  <th style={thStyle}>{t("analytics.person.col.name")}</th>
                  <th style={thStyle}>{t("analytics.person.col.props")}</th>
                </tr>
              </thead>
              <tbody>
                {events.map((e, i) => (
                  <tr key={`${e.at}|${e.name}|${i}`} data-testid="analytics-person-event">
                    <td style={{ ...tdStyle, whiteSpace: "nowrap" }}>{formatMadrid(e.at)}</td>
                    <td style={tdStyle}>{e.surface}</td>
                    <td style={tdStyle}>
                      <code>{e.name}</code>
                    </td>
                    <td style={tdStyle}>{propsText(e.props)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {hasMore ? (
          <button
            type="button"
            style={loadingMore ? { ...buttonStyle, ...disabledButtonStyle, marginTop: 12 } : { ...buttonStyle, marginTop: 12 }}
            disabled={loadingMore}
            onClick={onLoadMore}
            data-testid="analytics-person-more"
          >
            {loadingMore ? t("analytics.person.loadingMore") : t("analytics.person.loadMore")}
          </button>
        ) : null}
      </section>
    </div>
  );
}
