/**
 * The Vitest environment is Node-only (no DOM): the dialog is checked by rendering it to
 * markup, and by walking the element tree of its stateless half to reach the handlers.
 */
import { isValidElement, type ReactElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { isLatinReason, verifyVenuesBatch } from "@/lib/admin/venueBulk";
import { VenueBulkVerifyDialog, VenueBulkVerifyDialogView } from "./VenueBulkVerifyDialog";

type Props = Record<string, unknown> & { children?: ReactNode; "data-testid"?: string };

function find(node: ReactNode, testId: string): ReactElement<Props> | null {
  if (!isValidElement(node)) return null;
  const el = node as ReactElement<Props>;
  if (el.props["data-testid"] === testId) return el;
  const kids = el.props.children;
  for (const k of Array.isArray(kids) ? kids : [kids]) {
    const hit = find(k as ReactNode, testId);
    if (hit !== null) return hit;
  }
  return null;
}

function view(reason: string, over: Partial<Parameters<typeof VenueBulkVerifyDialogView>[0]> = {}) {
  return VenueBulkVerifyDialogView({
    count: 3,
    reason,
    onReasonChange: () => undefined,
    onCancel: () => undefined,
    onConfirm: () => undefined,
    ...over,
  });
}

describe("isLatinReason", () => {
  it("accepts Latin text and rejects empty and non-Latin text", () => {
    expect(isLatinReason("Checked address, ticket #4827")).toBe(true);
    expect(isLatinReason("   ")).toBe(false);
    expect(isLatinReason("проверено")).toBe(false);
    expect(isLatinReason("ok проверено")).toBe(false);
  });
});

describe("VenueBulkVerifyDialog", () => {
  it("opens with the venue count, the new status and an empty reason", () => {
    const html = renderToStaticMarkup(
      <VenueBulkVerifyDialog count={7} onCancel={() => undefined} onConfirm={() => undefined} />,
    );
    expect(html).toContain("You mark 7 venues as verified.");
    expect(html).toContain("<strong>verified</strong>");
    expect(html).toMatch(/data-testid="venues-bulk-dialog-reason"[^>]*value=""|value=""[^>]*data-testid="venues-bulk-dialog-reason"/);
  });

  it("starts with an empty reason on every open", () => {
    const render = () =>
      renderToStaticMarkup(
        <VenueBulkVerifyDialog count={2} onCancel={() => undefined} onConfirm={() => undefined} />,
      );
    expect(render()).toBe(render());
    expect(render()).toContain('value=""');
  });

  it("keeps confirm disabled until the reason is valid", () => {
    expect(find(view(""), "venues-bulk-dialog-confirm")?.props.disabled).toBe(true);
    expect(find(view("проверено"), "venues-bulk-dialog-confirm")?.props.disabled).toBe(true);
    expect(find(view("Checked address"), "venues-bulk-dialog-confirm")?.props.disabled).toBe(false);
    expect(find(view("проверено"), "venues-bulk-dialog-problem")).not.toBeNull();
  });

  it("cancel calls only onCancel", () => {
    const onCancel = vi.fn();
    const onConfirm = vi.fn();
    const cancel = find(view("Checked", { onCancel, onConfirm }), "venues-bulk-dialog-cancel");
    (cancel?.props.onClick as () => void)();
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("confirm hands over the typed reason, and the batch sends one request per venue with it", async () => {
    const onConfirm = vi.fn();
    const confirm = find(view("  Checked address  ", { onConfirm }), "venues-bulk-dialog-confirm");
    (confirm?.props.onClick as () => void)();
    expect(onConfirm).toHaveBeenCalledWith("Checked address");

    const patch = vi.fn().mockResolvedValue({});
    const venues = [
      { id: "a", name: "A", city_id: null, review_status: "unverified" },
      { id: "b", name: "B", city_id: null, review_status: "unverified" },
    ];
    const result = await verifyVenuesBatch({ venues, reason: onConfirm.mock.calls[0][0] as string, patch });
    expect(patch).toHaveBeenCalledTimes(2);
    expect(patch).toHaveBeenCalledWith("a", "Checked address");
    expect(patch).toHaveBeenCalledWith("b", "Checked address");
    expect(result.verified.sort()).toEqual(["a", "b"]);
  });
});
