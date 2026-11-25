/**
 * Stripe's Payment Element never becomes ready inside a ShadowRoot, so the
 * widget mounts it into LIGHT-DOM nodes and projects them into the shadow tree
 * through named slots. Outside a shadow root (tests, plain Svelte use) the
 * placeholders themselves are the mount points.
 */
export interface LightDomMounts {
  express: HTMLElement | null;
  payment: HTMLElement | null;
  /** Removes the light-DOM nodes this call created (no-op without shadow). */
  cleanup: () => void;
}

function lightNode(placeholder: HTMLElement | null, slotName: string): HTMLElement | null {
  if (!placeholder) return null;
  const root = placeholder.getRootNode();
  if (typeof ShadowRoot === 'undefined' || !(root instanceof ShadowRoot)) return null;
  const div = placeholder.ownerDocument.createElement('div');
  div.setAttribute('slot', slotName);
  div.style.width = '100%';
  root.host.appendChild(div);
  return div;
}

export function mountInLightDom(
  expressPlaceholder: HTMLElement | null,
  paymentPlaceholder: HTMLElement | null,
  expressSlot: string,
  paymentSlot: string,
): LightDomMounts {
  const e = lightNode(expressPlaceholder, expressSlot);
  const p = lightNode(paymentPlaceholder, paymentSlot);
  return {
    express: e ?? expressPlaceholder,
    payment: p ?? paymentPlaceholder,
    cleanup: () => {
      e?.remove();
      p?.remove();
    },
  };
}
