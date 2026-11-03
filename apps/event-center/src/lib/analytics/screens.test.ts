import { describe, expect, it } from "vitest";
import { createMemoryHistory, createRouter } from "@tanstack/react-router";
import { routeTree } from "@/router";
import { orgIdOf, screenOf, screenTemplate } from "@/lib/analytics/screens";
import { wizardStepOfField } from "@/lib/analytics/flows";

const ORG = "3c1a7d5e-0000-4000-8000-000000000001";
const EVENT = "9d2b0c11-0000-4000-8000-0000000000e1";
const SESSION = "5e7f0a22-0000-4000-8000-0000000000a5";

describe("screenTemplate", () => {
  it("drops the organization prefix and keeps the route parameters as names", () => {
    expect(screenTemplate("/o/$orgId/events/$eventId")).toBe("/events/$eventId");
    expect(screenTemplate("/o/$orgId/events/$eventId/sessions/$sessionId/seats")).toBe("/events/$eventId/sessions/$sessionId/seats");
    expect(screenTemplate("/o/$orgId/events")).toBe("/events");
  });
  it("names the organization home, the root and the screens outside an organization", () => {
    expect(screenTemplate("/o/$orgId/")).toBe("/");
    expect(screenTemplate("/o/$orgId")).toBe("/");
    expect(screenTemplate("__root__")).toBe("/not_found");
    expect(screenTemplate("/orgs")).toBe("/orgs");
    expect(screenTemplate("/login/link")).toBe("/login/link");
  });
});

describe("the real route tree", () => {
  async function at(path: string) {
    const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
    await router.load();
    return router.state.matches;
  }

  it("reports the template and the organization, never the concrete ids", async () => {
    const matches = await at(`/o/${ORG}/events/${EVENT}?tab=sales#top`);
    expect(screenOf(matches)).toBe("/events/$eventId");
    expect(orgIdOf(matches)).toBe(ORG);
  });

  it("covers a nested screen and a creation screen", async () => {
    expect(screenOf(await at(`/o/${ORG}/events/${EVENT}/sessions/${SESSION}/invitations`))).toBe("/events/$eventId/sessions/$sessionId/invitations");
    expect(screenOf(await at(`/o/${ORG}/events/new`))).toBe("/events/new");
    expect(screenOf(await at(`/o/${ORG}/links/new`))).toBe("/links/new");
    expect(screenOf(await at(`/o/${ORG}/team`))).toBe("/team");
  });

  it("has no organization on a screen outside one", async () => {
    const matches = await at("/orgs");
    expect(screenOf(matches)).toBe("/orgs");
    expect(orgIdOf(matches)).toBeUndefined();
  });

  it("an unknown address is reported as one fixed screen, not as its path", async () => {
    const matches = await at(`/o/${ORG}/no/such/page/${EVENT}`);
    const name = screenOf(matches);
    expect(name).not.toContain(EVENT);
    expect(name).not.toContain("such");
  });

  it("no route of the app produces a screen name with an id in it", async () => {
    const ids = Object.keys((createRouter({ routeTree }) as unknown as { routesById: Record<string, unknown> }).routesById);
    expect(ids.length).toBeGreaterThan(30);
    for (const id of ids) {
      const name = screenTemplate(id);
      expect(name).not.toMatch(/[0-9a-f]{8}-[0-9a-f]{4}-/i);
      expect(name).not.toContain("/o/$orgId");
    }
  });
});

describe("wizardStepOfField", () => {
  it("maps a touched field to the section the person is working in", () => {
    expect(wizardStepOfField("name")).toBe("event");
    expect(wizardStepOfField("currency")).toBe("event");
    expect(wizardStepOfField("venueName")).toBe("venue");
    expect(wizardStepOfField("venueExisting")).toBe("venue");
    expect(wizardStepOfField("seating.cat.0.price")).toBe("seating");
    expect(wizardStepOfField("d1.day")).toBe("dates");
    expect(wizardStepOfField("d1.c:abc.price")).toBe("tickets");
    expect(wizardStepOfField("d1.s:xyz.from")).toBe("tickets");
  });
});
