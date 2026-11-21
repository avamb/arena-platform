/**
 * One section of the event card: a heading that is a real button (`aria-expanded`, `aria-controls`) and a panel.
 * A closed panel stays in the DOM with `hidden`, but only after it was opened once: a section nobody opened never
 * mounts its blocks, so their requests are not sent. Once mounted, the blocks keep what the person typed when the
 * section closes. `useSectionState` holds the open state: the choice is remembered on this device per section id
 * (not per event), and an address hash that points into a closed section opens it for this visit.
 * Test ids: `event-section-<id>`, `event-section-toggle-<id>`, `event-section-panel-<id>`.
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { SECTION_IDS, readSectionOpen, sectionForAnchor, writeSectionOpen, type SectionId } from "@/lib/eventActions";

export interface SectionState {
  /** Is the section open: the person's own choice first, else the default, and always when the hash points into it. */
  isOpen: (id: SectionId, defaultOpen: boolean) => boolean;
  toggle: (id: SectionId, defaultOpen: boolean) => void;
}

export function useSectionState(hash: string): SectionState {
  const [chosen, setChosen] = useState<Partial<Record<SectionId, boolean>>>(() =>
    Object.fromEntries(SECTION_IDS.flatMap((id) => {
      const v = readSectionOpen(id);
      return v === null ? [] : [[id, v] as const];
    })),
  );
  const [forced, setForced] = useState<ReadonlySet<SectionId>>(new Set());
  useEffect(() => {
    const target = sectionForAnchor(hash);
    if (target !== null) setForced((prev) => new Set(prev).add(target));
  }, [hash]);
  const isOpen = useCallback((id: SectionId, defaultOpen: boolean) => forced.has(id) || (chosen[id] ?? defaultOpen), [forced, chosen]);
  const toggle = useCallback(
    (id: SectionId, defaultOpen: boolean) => {
      const next = !isOpen(id, defaultOpen);
      writeSectionOpen(id, next);
      setChosen((c) => ({ ...c, [id]: next }));
      setForced((prev) => {
        const n = new Set(prev);
        n.delete(id);
        return n;
      });
    },
    [isOpen],
  );
  return { isOpen, toggle };
}

/** How long the anchor scroll keeps the block in view while the layout above it still changes. */
export const ANCHOR_HOLD_MS = 8000;
const ANCHOR_TOLERANCE_PX = 2;

/**
 * Scrolls to the block an address hash names, once its section is open and the block is in the page, and keeps it
 * there while the layout above it changes (blocks mount on the first open and load their data). The hold ends after
 * ANCHOR_HOLD_MS, or at once when the person wheels, touches, presses a key or a pointer: it never fights the person.
 * The scroll is instant (no smooth animation), so it also respects prefers-reduced-motion.
 */
export function ScrollToAnchor({ hash, open }: { hash: string; open: boolean }) {
  useEffect(() => {
    if (hash === "" || !open) return;
    let name = hash.replace(/^#/, "");
    try {
      name = decodeURIComponent(name);
    } catch {
      // A hash that is not valid percent-encoding is used as it is.
    }
    if (name === "") return;
    let stopped = false;
    const scroll = (force: boolean) => {
      if (stopped) return;
      const el = document.getElementById(name);
      if (!el) return;
      if (!force && Math.abs(el.getBoundingClientRect().top) <= ANCHOR_TOLERANCE_PX) return;
      el.scrollIntoView({ behavior: "auto", block: "start" });
    };
    scroll(true);
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(() => scroll(false));
    observer?.observe(document.body);
    const events = ["wheel", "touchstart", "keydown", "pointerdown"] as const;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const stop = () => {
      stopped = true;
      observer?.disconnect();
      if (timer !== undefined) clearTimeout(timer);
      for (const e of events) window.removeEventListener(e, stop, true);
    };
    for (const e of events) window.addEventListener(e, stop, { capture: true, passive: true });
    timer = setTimeout(stop, ANCHOR_HOLD_MS);
    return stop;
  }, [hash, open]);
  return null;
}

export function EventSection({
  id,
  title,
  open,
  onToggle,
  children,
}: {
  id: SectionId;
  title: string;
  open: boolean;
  onToggle: () => void;
  children: ReactNode;
}) {
  const mounted = useRef(open);
  if (open) mounted.current = true;
  const buttonId = `event-section-toggle-${id}`;
  const panelId = `event-section-panel-${id}`;
  return (
    <section id={`event-section-${id}`} className="event-section" data-testid={`event-section-${id}`} data-open={open ? "1" : "0"}>
      <h2 className="event-section-head">
        <button
          type="button"
          id={buttonId}
          className="event-section-toggle"
          aria-expanded={open}
          aria-controls={panelId}
          onClick={onToggle}
          data-testid={buttonId}
        >
          <span>{title}</span>
          <span aria-hidden="true">{open ? "▴" : "▾"}</span>
        </button>
      </h2>
      <div id={panelId} role="region" aria-labelledby={buttonId} className="event-section-panel" hidden={!open} data-testid={panelId}>
        {mounted.current && children}
      </div>
    </section>
  );
}
