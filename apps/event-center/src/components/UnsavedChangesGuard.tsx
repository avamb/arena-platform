/**
 * Stops a person from losing typed work (A-02): while `dirty` is true, closing or reloading the tab asks the
 * browser to confirm, and a move to another screen of the app opens a question first ("There are unsaved
 * changes. Leave?"). Nothing else changes: with a clean form the guard does nothing at all.
 *
 * A move the form makes itself (after a save that opened the new card, after a delete) must not ask: the caller
 * sets `allowLeave.current = true` right before it navigates.
 */
import { useBlocker } from "@tanstack/react-router";
import { useCallback, useRef, type MutableRefObject } from "react";
import { useDialogBehaviour } from "@/components/useDialogBehaviour";
import { useI18n } from "@/lib/i18n";

export function UnsavedChangesGuard({ dirty, allowLeave }: { dirty: boolean; allowLeave?: MutableRefObject<boolean> }) {
  const { t } = useI18n();
  const box = useRef<HTMLDivElement>(null);
  const stay = useRef<HTMLButtonElement>(null);
  const live = useRef(dirty);
  live.current = dirty;
  // Stable functions: the router re-registers its blocker whenever they change.
  const shouldBlock = useCallback(() => live.current && allowLeave?.current !== true, [allowLeave]);
  const blocker = useBlocker({ shouldBlockFn: shouldBlock, enableBeforeUnload: shouldBlock, withResolver: true });
  const open = blocker.status === "blocked";
  if (!open) return null;
  return <GuardDialog box={box} stay={stay} onStay={() => blocker.reset?.()} onLeave={() => blocker.proceed?.()} title={t("shop.unsaved.title")} explain={t("shop.unsaved.explain")} leave={t("shop.unsaved.leave")} stayLabel={t("shop.unsaved.stay")} />;
}

function GuardDialog({
  box,
  stay,
  onStay,
  onLeave,
  title,
  explain,
  leave,
  stayLabel,
}: {
  box: React.RefObject<HTMLDivElement>;
  stay: React.RefObject<HTMLButtonElement>;
  onStay: () => void;
  onLeave: () => void;
  title: string;
  explain: string;
  leave: string;
  stayLabel: string;
}) {
  // The focus starts on "Stay": Enter must never throw work away by habit.
  const { onKeyDown } = useDialogBehaviour(box, stay, onStay);
  return (
    <div className="dialog-backdrop">
      <div ref={box} className="dialog" role="alertdialog" aria-modal="true" aria-labelledby="unsaved-title" aria-describedby="unsaved-explain" data-testid="unsaved-dialog" onKeyDown={onKeyDown}>
        <h2 id="unsaved-title">{title}</h2>
        <p id="unsaved-explain">{explain}</p>
        <div className="dialog-actions">
          <button ref={stay} type="button" className="btn btn-primary" onClick={onStay} data-testid="unsaved-stay">
            {stayLabel}
          </button>
          <button type="button" className="btn" onClick={onLeave} data-testid="unsaved-leave">
            {leave}
          </button>
        </div>
      </div>
    </div>
  );
}
