import { describe, expect, it } from "vitest";
import {
  MAX_GUESTS,
  batchId,
  canRevoke,
  inviteSessionOk,
  inviteTierOk,
  letterNeedsAttention,
  letterSummary,
  letterTone,
  parseGuests,
  sendBlock,
  validEmail,
} from "@/lib/invitations";

describe("parseGuests (the bot's ParseInviteRecipients)", () => {
  it("reads name and e-mail in any order, and the e-mail alone", () => {
    const r = parseGuests("Anna Ivanova, Anna@Example.com\nbob@example.org\n<carol@example.net> Carol Smith");
    expect(r.problems).toEqual([]);
    expect(r.guests).toEqual([
      { name: "Anna Ivanova", email: "anna@example.com" },
      { name: "", email: "bob@example.org" },
      { name: "Carol Smith", email: "carol@example.net" },
    ]);
  });

  it("skips blank lines and drops a pasted list marker", () => {
    const r = parseGuests("\n1. Anna, a@example.com\r\n\r\n- Bob b@example.com\n• c@example.com\n");
    expect(r.problems).toEqual([]);
    expect(r.guests.map((g) => g.email)).toEqual(["a@example.com", "b@example.com", "c@example.com"]);
    expect(r.guests[1].name).toBe("Bob");
  });

  it("names each problem line by its position in the text", () => {
    const r = parseGuests("ok@example.com\nno address here\n\nx@example.com y@example.com\nbad@nodot\nOK@example.com");
    expect(r.guests.map((g) => g.email)).toEqual(["ok@example.com"]);
    expect(r.problems.map((p) => [p.line, p.reason])).toEqual([
      [2, "email"],
      [4, "several"],
      [5, "email"],
      [6, "repeat"],
    ]);
  });

  it("refuses a name that is too long", () => {
    const r = parseGuests(`${"N".repeat(101)} n@example.com`);
    expect(r.guests).toEqual([]);
    expect(r.problems[0].reason).toBe("name");
    expect(parseGuests(`${"N".repeat(100)} n@example.com`).problems).toEqual([]);
  });

  it("keeps the e-mail rule of the API", () => {
    expect(validEmail("a@b.co")).toBe(true);
    for (const bad of ["", "a@b", "a@.co", "a@b.", "@b.co", "a b@c.co", "a@@b.co", "a@b,c.co"]) {
      expect(validEmail(bad), bad).toBe(false);
    }
  });
});

describe("sendBlock", () => {
  const guests = (n: number) => parseGuests(Array.from({ length: n }, (_, i) => `g${i}@example.com`).join("\n"));
  it("blocks an empty list, problems, more than one round and more than the free places", () => {
    expect(sendBlock(parseGuests(""), 10)).toBe("none");
    expect(sendBlock(parseGuests("nonsense"), 10)).toBe("problems");
    expect(sendBlock(guests(MAX_GUESTS + 1), 500)).toBe("tooMany");
    expect(sendBlock(guests(3), 2)).toBe("overFree");
    expect(sendBlock(guests(3), 3)).toBeNull();
    expect(sendBlock(guests(MAX_GUESTS), null)).toBeNull();
  });
});

describe("batchId", () => {
  it("is stable per guest, so a retry replays and never mails twice", () => {
    expect(batchId("op1", 0)).toBe("web-op1-0");
    expect(batchId("op1", 7)).toBe("web-op1-7");
    expect(batchId("op1", 7)).toBe(batchId("op1", 7));
    expect(batchId("op1", 1)).not.toBe(batchId("op2", 1));
  });
});

describe("letterSummary", () => {
  const l = (status: string, stuck = false) => ({ status, stuck });
  it("shows none when no letter was queued", () => {
    expect(letterSummary(undefined)).toBe("none");
    expect(letterSummary([])).toBe("none");
  });
  it("maps one letter to its own state", () => {
    for (const s of ["queued", "sent", "delayed", "delivered", "opened", "failed"] as const) {
      expect(letterSummary([l(s)])).toBe(s);
    }
  });
  it("a stuck letter is stuck, and a failed one stays failed", () => {
    expect(letterSummary([l("sent", true)])).toBe("stuck");
    expect(letterSummary([l("failed", true)])).toBe("failed");
  });
  it("is the weakest letter of the invitation", () => {
    expect(letterSummary([l("opened"), l("delivered")])).toBe("delivered");
    expect(letterSummary([l("delivered"), l("failed")])).toBe("failed");
    expect(letterSummary([l("delivered"), l("queued")])).toBe("queued");
  });
  it("tones and attention", () => {
    expect(letterTone("failed")).toBe("bad");
    expect(letterTone("stuck")).toBe("bad");
    expect(letterTone("queued")).toBe("wait");
    expect(letterTone("delivered")).toBe("ok");
    expect(letterTone("sent")).toBe("neutral");
    expect(letterNeedsAttention("failed")).toBe(true);
    expect(letterNeedsAttention("stuck")).toBe(true);
    expect(letterNeedsAttention("delayed")).toBe(false);
  });
});

describe("annul and picker rules", () => {
  it("offers annul only for a valid invitation to a person who may issue", () => {
    expect(canRevoke("valid", true)).toBe(true);
    expect(canRevoke("valid", false)).toBe(false);
    expect(canRevoke("used", true)).toBe(false);
    expect(canRevoke("revoked", true)).toBe(false);
  });
  it("offers only general admission categories", () => {
    expect(inviteTierOk({ kind: "ga" })).toBe(true);
    expect(inviteTierOk({ kind: "seated" })).toBe(false);
    expect(inviteTierOk({ kind: null })).toBe(false);
  });
  it("offers only upcoming dates without a seating plan", () => {
    const now = new Date("2026-10-15T10:00:00Z");
    const base = { status: "scheduled", end_at: "2026-11-01T20:00:00Z", seating_plan_version_id: null };
    expect(inviteSessionOk(base, now)).toBe(true);
    expect(inviteSessionOk({ ...base, status: "cancelled" }, now)).toBe(false);
    expect(inviteSessionOk({ ...base, end_at: "2026-10-01T20:00:00Z" }, now)).toBe(false);
    expect(inviteSessionOk({ ...base, seating_plan_version_id: "v1" }, now)).toBe(false);
  });
});
