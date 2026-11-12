/**
 * The "Venues" screen (spec 64 section 2.1.1): the stepwise venue chooser,
 * then the card of the chosen venue. /o/{org}/venues/{venue} opens a card
 * directly. The seating plan intake (state, "Upload a seating plan", previews)
 * lands in `VenuePlanIntakeSlot` in the next task.
 */
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { VenueChooser } from "@/components/VenueChooser";
import { VenuePlanIntakeSlot } from "@/components/venuePlan/VenuePlanIntakeSlot";
import { Badge, ErrorBox, Loading } from "@/components/ui";
import { listSeatingPlanVersions, listVenueSeatingPlans, listVenues } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { mapLink, planSeats, usablePlans, type CardPlan } from "@/lib/venues/card";
import { directoryVenueOf, type DirectoryVenue } from "@/lib/venues/directoryApi";
import { isVerified } from "@/lib/venues/chooser";
import { useOrg } from "@/screens/OrgLayout";

/** The venues picked in this tab: the card shows them at once, without a second read. */
const picked = new Map<string, DirectoryVenue>();

async function readCardPlans(venueId: string, orgId: string): Promise<CardPlan[]> {
  const { seating_plans } = await listVenueSeatingPlans(venueId);
  const out: CardPlan[] = [];
  for (const p of usablePlans(seating_plans ?? [], orgId)) {
    let seats: number | null = null;
    try {
      seats = planSeats(p, (await listSeatingPlanVersions(p.id)).seating_plan_versions ?? []);
    } catch {
      seats = null;
    }
    out.push({ id: p.id, name: p.name, seats });
  }
  return out;
}

function VenueCard({ orgId, venueId, canPlans }: { orgId: string; venueId: string; canPlans: boolean }) {
  const { t } = useI18n();
  const known = picked.get(venueId);
  const list = useQuery({ queryKey: ["venues", orgId], queryFn: () => listVenues(orgId), enabled: known === undefined });
  const plans = useQuery({ queryKey: ["venueCard", "plans", orgId, venueId], queryFn: () => readCardPlans(venueId, orgId), enabled: canPlans, retry: false });

  let venue: DirectoryVenue | undefined = known;
  if (venue === undefined) {
    if (list.isLoading) return <Loading />;
    if (list.error !== null) return <ErrorBox error={list.error} onRetry={() => void list.refetch()} />;
    const found = list.data?.venues.find((v) => v.id === venueId);
    if (found !== undefined) venue = directoryVenueOf(found, orgId, "");
  }
  const back = (
    <p>
      <Link to="/o/$orgId/venues" params={{ orgId }} data-testid="venue-card-back">
        {t("venues.backToChooser")}
      </Link>
    </p>
  );
  if (venue === undefined) {
    return (
      <section>
        <p className="notice" role="status" data-testid="venue-gone">
          {t("venues.gone")}
        </p>
        {back}
      </section>
    );
  }
  const map = mapLink(venue.latitude, venue.longitude);
  const address = [venue.address_line1, venue.city_name].filter((x) => x !== "").join(", ");
  return (
    <section className="card venue-card" aria-labelledby="venue-card-title" data-testid="venue-card">
      {back}
      <h2 id="venue-card-title" data-testid="venue-card-name">
        {venue.name}
      </h2>
      {isVerified(venue) && (
        <p data-testid="venue-card-verified">
          <Badge tone="ok">{t("venues.mark.verified")}</Badge>
        </p>
      )}
      <p data-testid="venue-card-address">{address !== "" ? t("venues.card.address", { address }) : t("venues.card.noAddress")}</p>
      {map !== null && (
        <p>
          <a href={map} target="_blank" rel="noopener noreferrer" data-testid="venue-card-map">
            {t("venues.card.map")}
          </a>
        </p>
      )}
      <p data-testid="venue-card-zone">{venue.timezone !== "" ? t("venues.card.zone", { zone: venue.timezone }) : t("venues.card.noZone")}</p>
      {venue.capacity_default !== null && <p data-testid="venue-card-seats">{t("venues.card.seats", { seats: venue.capacity_default })}</p>}

      {canPlans && (
        <>
          <h3>{t("venues.card.plans")}</h3>
          {plans.isLoading && <Loading />}
          {plans.isError && (
            <p className="notice notice-bad" role="alert" data-testid="venue-card-plans-failed">
              {t("venues.card.plansFailed")}
            </p>
          )}
          {plans.isSuccess && plans.data.length === 0 && (
            <p className="muted" data-testid="venue-card-no-plans">
              {t("venues.card.noPlans")}
            </p>
          )}
          {plans.isSuccess && plans.data.length > 0 && (
            <ul className="chooser-list" data-testid="venue-card-plans">
              {plans.data.map((p) => (
                <li key={p.id} data-testid={`venue-card-plan-${p.id}`}>
                  <span className="chooser-row-name">{p.name}</span>
                  {p.seats !== null && <span className="muted"> {t("venues.card.planSeats", { count: p.seats })}</span>}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      <VenuePlanIntakeSlot key={venue.id} orgId={orgId} venueId={venue.id} venueName={venue.name} />
    </section>
  );
}

export function VenuesScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const navigate = useNavigate();
  const { venueId } = useParams({ strict: false }) as { venueId?: string };

  if (!access.can("venue.read")) {
    return (
      <section>
        <h1>{t("venues.title")}</h1>
        <p className="notice notice-bad" role="status" data-testid="venues-no-access">
          {t("venues.noAccess")}
        </p>
      </section>
    );
  }
  return (
    <section data-testid="venues-screen">
      <h1>{t("venues.title")}</h1>
      {venueId === undefined || venueId === "" ? (
        <>
          <p className="muted">{t("venues.intro")}</p>
          <VenueChooser
            orgId={orgId}
            onPick={(v) => {
              picked.set(v.id, v);
              void navigate({ to: "/o/$orgId/venues/$venueId", params: { orgId, venueId: v.id } });
            }}
          />
        </>
      ) : (
        <VenueCard key={venueId} orgId={orgId} venueId={venueId} canPlans={access.can("seating_plan.read")} />
      )}
    </section>
  );
}
