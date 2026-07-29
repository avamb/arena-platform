import { describe, expect, it } from "vitest";
import { buildOrgContextHref, readOrgContext } from "./orgContext";

describe("organization URL context", () => {
  it("round-trips an organization through a CRUD deep link", () => {
    const id = "00000000-0000-0000-0000-000000000001";
    const href = buildOrgContextHref("/channels", id);
    expect(href).toBe(`/channels?org=${id}`);
    expect(readOrgContext(href.slice(href.indexOf("?")))).toBe(id);
  });

  it("returns an empty context when no organization was supplied", () => {
    expect(readOrgContext("")).toBe("");
    expect(readOrgContext("?tab=venues")).toBe("");
  });
});
