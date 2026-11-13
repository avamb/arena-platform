/**
 * A box that shows its content large and lets the person move and zoom it:
 * drag with the pointer (finger or mouse), the wheel, the +/- buttons, the keys
 * (+, -, 0 and the arrows) and "Show all". The maths is lib/venuePlan/panZoom.ts.
 * The transform is written through the CSSOM: the page's CSP has no inline style.
 */
import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
import { useI18n } from "@/lib/i18n";
import { KEY_PAN, RESET, isReset, panBy, transformOf, wheelFactor, zoomAt, zoomStep, type Box, type View } from "@/lib/venuePlan/panZoom";

export function PanZoom({ children, testId = "plan-zoom" }: { children: ReactNode; testId?: string }) {
  const { t } = useI18n();
  const box = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const [view, setView] = useState<View>(RESET);
  const [dragging, setDragging] = useState(false);
  const drag = useRef<{ id: number; x: number; y: number } | null>(null);
  // The wheel listener is native (not passive): a React onWheel cannot stop the page from scrolling.
  const latest = useRef(view);
  latest.current = view;

  const size = (): Box => {
    const r = box.current?.getBoundingClientRect();
    return { w: r?.width ?? 1, h: r?.height ?? 1 };
  };

  useLayoutEffect(() => {
    if (content.current !== null) content.current.style.transform = transformOf(view);
  }, [view]);

  useEffect(() => {
    const el = box.current;
    if (el === null) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = el.getBoundingClientRect();
      setView(zoomAt(latest.current, wheelFactor(e.deltaY), e.clientX - r.left, e.clientY - r.top, { w: r.width, h: r.height }));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, []);

  const down = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    drag.current = { id: e.pointerId, x: e.clientX, y: e.clientY };
    e.currentTarget.setPointerCapture?.(e.pointerId);
    setDragging(true);
  };
  const move = (e: ReactPointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (d === null || d.id !== e.pointerId) return;
    const dx = e.clientX - d.x;
    const dy = e.clientY - d.y;
    drag.current = { id: d.id, x: e.clientX, y: e.clientY };
    setView((v) => panBy(v, dx, dy, size()));
  };
  const up = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (drag.current?.id === e.pointerId) {
      drag.current = null;
      setDragging(false);
    }
  };
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    const b = size();
    if (e.key === "+" || e.key === "=") setView((v) => zoomStep(v, 1, b));
    else if (e.key === "-") setView((v) => zoomStep(v, -1, b));
    else if (e.key === "0") setView(RESET);
    else if (e.key === "ArrowLeft") setView((v) => panBy(v, b.w * KEY_PAN, 0, b));
    else if (e.key === "ArrowRight") setView((v) => panBy(v, -b.w * KEY_PAN, 0, b));
    else if (e.key === "ArrowUp") setView((v) => panBy(v, 0, b.h * KEY_PAN, b));
    else if (e.key === "ArrowDown") setView((v) => panBy(v, 0, -b.h * KEY_PAN, b));
    else return;
    e.preventDefault();
  };

  return (
    <div data-testid={testId}>
      <div className="plan-zoom-tools">
        <button type="button" className="btn" onClick={() => setView((v) => zoomStep(v, 1, size()))} data-testid={`${testId}-in`}>
          {t("venuePlan.zoom.in")}
        </button>
        <button type="button" className="btn" onClick={() => setView((v) => zoomStep(v, -1, size()))} data-testid={`${testId}-out`}>
          {t("venuePlan.zoom.out")}
        </button>
        <button type="button" className="btn" disabled={isReset(view)} onClick={() => setView(RESET)} data-testid={`${testId}-reset`}>
          {t("venuePlan.zoom.reset")}
        </button>
      </div>
      <div
        ref={box}
        className={dragging ? "plan-zoom plan-zoom-dragging" : "plan-zoom"}
        role="group"
        aria-label={t("venuePlan.zoom.region")}
        aria-describedby={`${testId}-hint`}
        tabIndex={0}
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={up}
        onPointerCancel={up}
        onKeyDown={key}
        data-testid={`${testId}-box`}
      >
        <div ref={content} className="plan-zoom-content" data-scale={view.scale.toFixed(2)}>
          {children}
        </div>
      </div>
      <p className="field-hint" id={`${testId}-hint`}>
        {t("venuePlan.zoom.hint")}
      </p>
    </div>
  );
}
