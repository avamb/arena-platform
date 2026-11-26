// @vitest-environment jsdom
/**
 * The posters switch of the thank-you page's "You may also like" block sits on
 * the channel card: it shows the stored value, and one click sends ONLY
 * `thank_you_posters` (never the contact, never the operator's switches).
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@/lib/i18n";
import type { SellerProfileChannel } from "@/lib/api/types";

const patchSellerDisplay = vi.fn();
vi.mock("@/lib/api/endpoints", () => ({ patchSellerDisplay: (...a: unknown[]) => patchSellerDisplay(...a) }));

import { SellerChannelCard } from "@/components/SellerChannelCard";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const channel = (posters: boolean): SellerProfileChannel => ({
  id: "c1",
  name: "Tickets page",
  show_seller: false,
  seller_hidden: false,
  complete: true,
  seller_shown: true,
  consent_required: false,
  consent_active: false,
  thank_you_posters: posters,
  public_contact: null,
  email_looks_personal: false,
  missing: [],
});

function mount(c: SellerProfileChannel, locale: "en" | "ru" = "en") {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  const qc = new QueryClient();
  act(() =>
    root?.render(
      <QueryClientProvider client={qc}>
        <I18nProvider locale={locale}>
          <ul>
            <SellerChannelCard orgId="o1" channel={c} />
          </ul>
        </I18nProvider>
      </QueryClientProvider>,
    ),
  );
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  patchSellerDisplay.mockReset();
});

const box = () => host?.querySelector<HTMLInputElement>('[data-testid="seller-thank-you-posters"]') ?? null;

describe("the thank-you posters switch on the channel card", () => {
  it("shows the stored value, with the label in the owner's language", () => {
    mount(channel(true), "ru");
    expect(box()?.checked).toBe(true);
    expect(host?.textContent).toContain("Показывать афиши в блоке «Вам может понравиться» после покупки");
  });

  it("is unticked when the channel has posters off", () => {
    mount(channel(false));
    expect(box()?.checked).toBe(false);
    expect(host?.textContent).toContain("Show posters in the");
  });

  it("sends only thank_you_posters when it is clicked", async () => {
    patchSellerDisplay.mockResolvedValue({});
    mount(channel(true));
    await act(async () => {
      box()?.click();
    });
    expect(patchSellerDisplay).toHaveBeenCalledTimes(1);
    expect(patchSellerDisplay).toHaveBeenCalledWith("o1", "c1", { thank_you_posters: false });
  });
});
