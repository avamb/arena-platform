/**
 * Stops a person from losing typed work (A-02): while `dirty` is true, closing or reloading the tab asks the
 * browser to confirm, and a move to another screen of the app opens a question first ("There are unsaved
 * changes. Leave?"). Nothing else changes: with a clean form the guard does nothing at all.
 *
 * A move the form makes itself (after a save that opened the new card, after a delete) must not ask: the caller
 * sets `allowLeave.current = true` right before it navigates.
 *
 * M-09: the question sits ABOVE everything (the sticky Save bar included), and `onOpenChange` tells the screen
 * when it is open so the screen can hide the bar. With `onSaveAndLeave` the question has a third button: it runs
 * the card's one Save, then leaves when the save went through (and stays, showing what is wrong, when it did not).
 */
import { useBlocker } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState, type MutableRefObject } from "react";
import { useDialogBehaviour } from "@/components/useDialogBehaviour";
import { useI18n } from "@/lib/i18n";

export interface UnsavedChangesGuardProps {
  dirty: boolean;
  allowLeave?: MutableRefObject<boolean>;
  /** Called with true while the question is open, false when it closes. */
  onOpenChange?: (open: boolean) => void;
  /** Runs the card's Save; true when everything was stored. Without it the question has two buttons. */
  onSaveAndLeave?: () => Promise<boolean>;
}

export function UnsavedChangesGuard({ dirty, allowLeave, onOpenChange, onSaveAndLeave }: UnsavedChangesGuardProps) {
  const { t } = useI18n();
  const box = useRef<HTMLDivElement>(null);
  const stay = useRef<HTMLButtonElement>(null);
  const live = useRef(dirty);
  live.current = dirty;
  const [saving, setSaving] = useState(false);
  // Stable functions: the router re-registers its blocker whenever they change.
  const shouldBlock = useCallback(() => live.current && allowLeave?.current !== true, [allowLeave]);
  const blocker = useBlocker({ shouldBlockFn: shouldBlock, enableBeforeUnload: shouldBlock, withResolver: true });
  const open = blocker.status === "blocked";
  const tell = useRef(onOpenChange);
  tell.current = onOpenChange;
  useEffect(() => {
    tell.current?.(open);
    return () => {
      if (open) tell.current?.(false);
    };
  }, [open]);
  if (!open) return null;
  const saveAndLeave =
    onSaveAndLeave === undefined
      ? undefined
      : async () => {
          setSaving(true);
          let ok = false;
          try {
            ok = await onSaveAndLeave();
          } finally {
            setSaving(false);
          }
          if (ok) {
            if (allowLeave !== undefined) allowLeave.current = true;
            blocker.proceed?.();
          } else blocker.reset?.();
        };
  return (
    <GuardDialog
      box={box}
      stay={stay}
      busy={saving}
      onStay={() => blocker.reset?.()}
      onLeave={() => blocker.proceed?.()}
      onSaveAndLeave={saveAndLeave}
      title={t("shop.unsaved.title")}
      explain={t("shop.unsaved.explain")}
      leave={t("shop.unsaved.leave")}
      stayLabel={t("shop.unsaved.stay")}
      saveLabel={t("shop.unsaved.saveLeave")}
    />
  );
}

function GuardDialog({
  box,
  stay,
  busy,
  onStay,
  onLeave,
  onSaveAndLeave,
  title,
  explain,
  leave,
  stayLabel,
  saveLabel,
}: {
  box: React.RefObject<HTMLDivElement>;
  stay: React.RefObject<HTMLButtonElement>;
  busy: boolean;
  onStay: () => void;
  onLeave: () => void;
  onSaveAndLeave?: () => void;
  title: string;
  explain: string;
  leave: string;
  stayLabel: string;
  saveLabel: string;
}) {
  // The focus starts on "Stay": Enter must never throw work away by habit.
  const { onKeyDown } = useDialogBehaviour(box, stay, onStay);
  return (
    <div className="dialog-backdrop">
      <div ref={box} className="dialog" role="alertdialog" aria-modal="true" aria-labelledby="unsaved-title" aria-describedby="unsaved-explain" data-testid="unsaved-dialog" onKeyDown={onKeyDown}>
        <h2 id="unsaved-title">{title}</h2>
        <p id="unsaved-explain">{explain}</p>
        <div className="dialog-actions">
          <button ref={stay} type="button" className="btn btn-primary" disabled={busy} onClick={onStay} data-testid="unsaved-stay">
            {stayLabel}
          </button>
          {onSaveAndLeave !== undefined && (
            <button type="button" className="btn" disabled={busy} onClick={onSaveAndLeave} data-testid="unsaved-save-leave">
              {saveLabel}
            </button>
          )}
          <button type="button" className="btn" disabled={busy} onClick={onLeave} data-testid="unsaved-leave">
            {leave}
          </button>
        </div>
      </div>
    </div>
  );
}
