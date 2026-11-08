/**
 * The visible "Save changes" button of an editing page: ONE shared bar for every screen that tracks unsaved
 * changes. It is disabled while nothing changed, enabled as soon as something did, saves in place (the page
 * stays) and goes back to disabled when the save went through. The existing leave question
 * (`UnsavedChangesGuard`) stays as it is; the screen passes `hidden` while that question is open.
 *
 * On a wide screen the bar sticks to the top of the page, on a phone it sits at the bottom, above the tab bar
 * (styles: `.shop-savebar`). The screen adds the class `has-save-bar` to its container so the last block is never
 * covered.
 */
import { useI18n, type MessageKey } from "@/lib/i18n";

export type SaveBarTone = "ok" | "bad" | "plain";

export interface SaveBarProps {
  /** Something was changed and not saved yet. */
  dirty: boolean;
  /** A save is on its way. */
  busy: boolean;
  onSave: () => void;
  /** A refusal the page already knows about (a field that is wrong): the button stays disabled. */
  blocked?: boolean;
  /** The line beside the button; without one the bar says "unsaved changes" or nothing. */
  status?: { key: MessageKey; tone: SaveBarTone } | null;
  /** Extra content after the status line (e.g. "already saved: ..."). */
  extra?: React.ReactNode;
  /** Hidden but keeping its room, while the leave question is open. */
  hidden?: boolean;
  /** Test id of the button; the bar is `<id>-bar` and its line `<id>-status`. */
  testId?: string;
  /** Overrides of the bar's and the line's test ids (the older shop screens keep theirs). */
  barTestId?: string;
  statusTestId?: string;
  /** Button caption overrides (catalog keys). */
  labelKey?: MessageKey;
  busyKey?: MessageKey;
}

export function SaveBar({ dirty, busy, onSave, blocked = false, status, extra, hidden = false, testId = "save-changes", barTestId, statusTestId, labelKey = "saveBar.save", busyKey = "saveBar.saving" }: SaveBarProps) {
  const { t } = useI18n();
  const line = status !== undefined ? status : dirty ? { key: "saveBar.dirty" as MessageKey, tone: "plain" as const } : null;
  return (
    <div className="shop-savebar save-bar" data-testid={barTestId ?? `${testId}-bar`} hidden={hidden} aria-hidden={hidden ? true : undefined}>
      <button type="button" className="btn btn-primary" disabled={busy || !dirty || blocked} onClick={onSave} data-testid={testId}>
        {busy ? t(busyKey) : t(labelKey)}
      </button>
      <p className={`shop-savebar-status${line?.tone === "bad" ? " field-error" : ""}`} role="status" aria-live="polite" data-testid={statusTestId ?? `${testId}-status`}>
        {line !== null && t(line.key)}
        {extra}
      </p>
    </div>
  );
}
