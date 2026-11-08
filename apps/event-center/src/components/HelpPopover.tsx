/**
 * The round "?" next to a section title (ported from the WordPress event center, FX8): a short explanation that
 * opens on a tap or click, so it works on a phone, where a `title` hint never shows.
 *
 * - A real button: Enter and Space open it, `aria-expanded` and `aria-controls` tell a screen reader what it does,
 *   and the text sits in a `role="note"` region right after it, rendered only while it is open.
 * - It closes on a second press, on a press outside, on Escape (the focus returns to the "?") and when another
 *   popover opens: only one is open at a time.
 * - The text comes from the catalog (`help.*`), so en, ru and es read the same way.
 */
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { useI18n, type MessageKey } from "@/lib/i18n";

/** One popover at a time: the one that opens announces itself, the others close. */
const OPENED = "arena-help-opened";

export interface HelpPopoverProps {
  /** The catalog key of the explanation (`help.*`). */
  textKey: MessageKey;
  /** Placeholders of the text, when it has any. */
  values?: Record<string, string | number>;
  /** Extra class, e.g. to sit inline in a line of text. */
  className?: string;
  testId?: string;
}

export function HelpPopover({ textKey, values, className, testId = "help" }: HelpPopoverProps) {
  const { t } = useI18n();
  const id = useId();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLSpanElement>(null);
  const button = useRef<HTMLButtonElement>(null);

  const close = useCallback((refocus: boolean) => {
    setOpen(false);
    if (refocus) button.current?.focus();
  }, []);

  useEffect(() => {
    if (!open) return;
    const onPointer = (e: Event) => {
      if (root.current !== null && e.target instanceof Node && !root.current.contains(e.target)) close(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close(true);
    };
    const onOther = (e: Event) => {
      if ((e as CustomEvent<string>).detail !== id) close(false);
    };
    document.addEventListener("pointerdown", onPointer);
    document.addEventListener("mousedown", onPointer);
    document.addEventListener("keydown", onKey);
    window.addEventListener(OPENED, onOther);
    return () => {
      document.removeEventListener("pointerdown", onPointer);
      document.removeEventListener("mousedown", onPointer);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener(OPENED, onOther);
    };
  }, [open, id, close]);

  const toggle = () => {
    if (open) return close(false);
    window.dispatchEvent(new CustomEvent(OPENED, { detail: id }));
    setOpen(true);
  };

  return (
    <span ref={root} className={`help${className !== undefined ? ` ${className}` : ""}`} data-testid={testId}>
      <button
        ref={button}
        type="button"
        className="help-btn"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        aria-label={t("help.open")}
        onClick={toggle}
        data-testid={`${testId}-button`}
      >
        ?
      </button>
      {/* Rendered only while open: a hidden note would still be found by text and would leak into the
          accessible name of a section labelled by its heading. */}
      {open && (
        <span id={id} role="note" className="help-pop" data-testid={`${testId}-text`}>
          {t(textKey, values)}
        </span>
      )}
    </span>
  );
}
