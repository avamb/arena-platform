import { afterEach, describe, expect, it, vi } from "vitest";
import { navGroups, promotionItems } from "@/lib/navMenu";

const OWNER = [
  "import.bil24_session",
  "order.read",
  "refund.read",
  "promo.read",
  "complimentary.read",
  "sales_link.read",
  "promoter.read",
  "venue.read",
  "membership.read",
  "billing.read",
  "org.update",
  "payment_config.read",
  "shop.read",
];

const can = (perms: string[]) => (p: string) => perms.includes(p);
const shape = (perms: string[], shop: "full" | "none" = "none", orgs = 1) =>
  navGroups({ can: can(perms) }, shop, orgs).map((g) => [g.id, g.items.map((i) => i.id)] as const);

afterEach(() => vi.unstubAllEnvs());

describe("navGroups", () => {
  it("gives the owner every section, in the order of the bot's menu", () => {
    vi.stubEnv("VITE_FEATURE_WIZARD", "1");
    expect(shape(OWNER, "full", 2)).toEqual([
      ["new", ["nav-new-event"]],
      ["events", ["menu-events", "nav-summary"]],
      ["orders", ["menu-orders", "menu-refunds"]],
      ["promotion", ["nav-promo", "nav-invitations", "nav-links", "nav-growth"]],
      ["entrance", ["nav-scanner", "nav-guest-list"]],
      ["shop", ["nav-shop"]],
      [
        "settings",
        ["nav-org-data", "nav-payments", "nav-team", "nav-promoters", "nav-venues", "nav-notifications", "nav-billing", "nav-switch-org"],
      ],
      ["help", ["nav-help"]],
    ]);
  });

  it("drops what a manager without org.update and billing.read cannot use", () => {
    vi.stubEnv("VITE_FEATURE_WIZARD", "1");
    const manager = OWNER.filter((p) => p !== "org.update" && p !== "billing.read" && p !== "payment_config.read");
    const groups = shape(manager);
    expect(groups.find(([id]) => id === "settings")?.[1]).toEqual(["nav-team", "nav-promoters", "nav-venues", "nav-notifications"]);
    expect(groups.map(([id]) => id)).not.toContain("shop");
  });

  it("leaves out Orders and the guest list for a role without order.read, and a section with no item", () => {
    const groups = shape(["promo.read"]);
    expect(groups.map(([id]) => id)).toEqual(["events", "promotion", "entrance", "settings", "help"]);
    expect(groups.find(([id]) => id === "entrance")?.[1]).toEqual(["nav-scanner"]);
    expect(groups.find(([id]) => id === "promotion")?.[1]).toEqual(["nav-promo", "nav-growth"]);
  });

  it("shows New event only when the wizard gate is open", () => {
    expect(shape(OWNER).map(([id]) => id)).not.toContain("new");
    vi.stubEnv("VITE_FEATURE_WIZARD", "1");
    expect(shape(["order.read"]).map(([id]) => id)).not.toContain("new");
    expect(shape(["import.bil24_session"]).map(([id]) => id)).toContain("new");
  });

  it("has no Promotion item for a role with none", () => {
    expect(promotionItems(can([]))).toEqual([]);
  });
});
