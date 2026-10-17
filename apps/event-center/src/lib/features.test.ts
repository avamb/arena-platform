import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { flagOn, wizardEnabled, wizardGate } from "@/lib/features";

describe("feature flags", () => {
  it("are on only for the exact value 1", () => {
    expect(flagOn("1")).toBe(true);
    for (const off of [undefined, "", "0", "true", "yes", " 1", 1, true, null]) expect(flagOn(off), String(off)).toBe(false);
  });
  it("keep the wizard off in a build nobody configured, and on when asked", () => {
    expect(wizardEnabled(undefined)).toBe(false);
    expect(wizardEnabled("0")).toBe(false);
    expect(wizardEnabled("1")).toBe(true);
  });
  it("ships on in the release image: the Dockerfile defaults the flag to 1 in both stages", () => {
    const docker = readFileSync(path.resolve(__dirname, "../../Dockerfile"), "utf-8");
    const args = docker.split(/\r?\n/).filter((l) => l.startsWith("ARG VITE_FEATURE_WIZARD"));
    expect(args).toEqual(["ARG VITE_FEATURE_WIZARD=1", "ARG VITE_FEATURE_WIZARD=1"]);
    expect(docker).toContain("ENV VITE_FEATURE_WIZARD=${VITE_FEATURE_WIZARD}");
  });

  it("routes a person: no wizard in the build, no permission, or the form", () => {
    const owner = (p: string) => p === "import.bil24_session";
    const agent = () => false;
    expect(wizardGate(owner, undefined)).toBe("off");
    expect(wizardGate(owner, "0")).toBe("off");
    expect(wizardGate(agent, "0")).toBe("off");
    expect(wizardGate(agent, "1")).toBe("noAccess");
    expect(wizardGate(owner, "1")).toBe("ok");
    // Only the import permission opens the form: another write permission does not.
    expect(wizardGate((p) => p === "event.write", "1")).toBe("noAccess");
  });
});
