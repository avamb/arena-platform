/**
 * The map of the seat manager: the hall as SVG, with pan, zoom and the ways of
 * picking seats. Touch is the main input (the screen opens inside Telegram), so:
 *
 *  - every ACTION is a finger-sized press: nothing happens on hover. Hover is
 *    allowed only because it merely informs: a mouse over a seat shows the
 *    seat card (the buyer's card, @seatmap/lib/seat-card) and paints nothing.
 *    Touch has no hover, so it gets the long press (about half a second on a
 *    seat, the finger staying put) that shows the same card and does not paint;
 *  - two fingers always move and zoom the map, whatever the tool is;
 *  - one finger moves the map with the "Move" tool, and in the picking tools
 *    (seat, row, section) a drag moves the map while a tap picks;
 *  - one finger DRAWS with the rectangle and lasso tools;
 *  - the mouse wheel zooms on a desktop.
 *
 * There are NO per-seat listeners: a plan can have thousands of seats, so the
 * hover and the long press go through the viewport's pointer handlers and the
 * `seatAt` index, and the card is plain DOM written directly (no React render,
 * no re-colouring of seats).
 *
 * The geometry of the gestures (pan, pinch, wheel, fit) is the widget's own
 * pan-zoom module, so the organizer's map behaves like the buyer's.
 */
import { forwardRef, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef } from "react";
import type { Geometry } from "@seatmap/types";
import "@seatmap/lib/seat-card.css";
import { createSeatCard, type SeatCard, type SeatCardModel } from "@seatmap/lib/seat-card";
import {
  IDENTITY,
  fitTransform,
  pan,
  pinchZoom,
  pointerDist,
  pointerMid,
  toCSSTransform,
  wheelZoom,
  type PanZoomState,
} from "@seatmap/lib/pan-zoom";
import {
  normalizeRect,
  rowSeatsOf,
  sectionSeatsOf,
  seatAt,
  seatsInPolygon,
  seatsInRect,
  type HallIndex,
  type Point,
} from "@/lib/seatManager/model";
import {
  buildHallSvg,
  collectSeatNodes,
  drawShape,
  drawStage,
  fitHandles,
  recolor,
  type SeatLook,
} from "@/lib/seatManager/render";

export type MapTool = "pan" | "seat" | "row" | "section" | "rect" | "lasso" | "stage";

export interface SeatMapHandle {
  fit: () => void;
  zoomBy: (factor: number) => void;
}

interface Props {
  geometry: Geometry;
  index: HallIndex;
  /** The accessible name of the map. */
  label: string;
  look: (seatKey: string) => SeatLook;
  /** What the seat card says about a seat, or null for no card. Called when the card is shown or refreshed. */
  cardOf?: (seatKey: string) => SeatCardModel | null;
  tool: MapTool;
  stage: Point | null;
  /** Seats picked by a tap or a drawn shape. */
  onSeats: (keys: string[], tool: MapTool) => void;
  /** A row handle was pressed. */
  onRowHandle: (rowId: string, additive: boolean) => void;
  /** The stage was pointed at (tool "stage"). */
  onStage: (p: Point) => void;
}

/** A finger moves a little while it taps: further than this it is a drag. */
const TAP_SLOP = 8;
/** Extra reach around a seat, in screen pixels: a seat is small under a finger. */
const SEAT_SLACK_PX = 10;
/** A finger that stays on a seat this long (inside the tap slop) asks the seat card instead of painting. */
export const LONG_PRESS_MS = 500;

/** "peek": the long press showed the card; lifting the finger paints nothing. */
type Mode = "none" | "tap" | "pan" | "pinch" | "draw" | "peek";

export const SeatMapCanvas = forwardRef<SeatMapHandle, Props>(function SeatMapCanvas(props, ref) {
  const { geometry, index, label } = props;
  const viewport = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  const nodes = useRef<Map<string, SVGCircleElement>>(new Map());
  const view = useRef<PanZoomState>(IDENTITY);
  const moved = useRef(false);
  const latest = useRef(props);
  latest.current = props;

  const svg = useMemo(() => buildHallSvg(geometry, index, label), [geometry, index, label]);

  // The seat card: plain DOM, written directly. Showing it never renders React and never re-colours a seat.
  const card = useRef<SeatCard | null>(null);
  const shownKey = useRef<string | null>(null);
  const hideCard = useCallback(() => {
    shownKey.current = null;
    card.current?.hide();
  }, []);

  const apply = useCallback(() => {
    const el = inner.current;
    if (el === null) return;
    // The map moved or zoomed: the card would point at the wrong place.
    hideCard();
    el.style.transform = toCSSTransform(view.current);
    fitHandles(el, latest.current.index.minHandleGap, view.current.scale);
    drawStage(el, latest.current.stage, view.current.scale);
    viewport.current?.setAttribute("data-scale", view.current.scale.toFixed(3));
  }, [hideCard]);

  const showCardFor = useCallback(
    (key: string) => {
      const c = card.current;
      const vp = viewport.current;
      const info = latest.current.index.byKey.get(key);
      const model = latest.current.cardOf?.(key) ?? null;
      if (c === null || vp === null || info === undefined || model === null) {
        hideCard();
        return;
      }
      const r = vp.getBoundingClientRect();
      const s = view.current.scale;
      const cx = r.left + view.current.x + info.x * s;
      const cy = r.top + view.current.y + info.y * s;
      const rad = info.radius * s;
      shownKey.current = key;
      c.show(model, { left: cx - rad, top: cy - rad, right: cx + rad, bottom: cy + rad });
    },
    [hideCard],
  );

  const fit = useCallback(() => {
    const vp = viewport.current;
    if (vp === null) return;
    const { width, height } = vp.getBoundingClientRect();
    view.current = fitTransform(latest.current.index.width, latest.current.index.height, width, height, 8);
    moved.current = false;
    apply();
  }, [apply]);

  useImperativeHandle(
    ref,
    () => ({
      fit,
      zoomBy: (factor: number) => {
        const vp = viewport.current;
        if (vp === null) return;
        const r = vp.getBoundingClientRect();
        view.current = wheelZoom(view.current, factor > 1 ? -1 : 1, r.width / 2, r.height / 2);
        moved.current = true;
        apply();
      },
    }),
    [fit, apply],
  );

  // A new hall: collect its seat nodes, paint them, fit the view.
  useLayoutEffect(() => {
    const el = inner.current;
    if (el === null) return;
    nodes.current = collectSeatNodes(el);
    recolor(nodes.current, latest.current.look);
    fit();
  }, [svg, fit]);

  // The tray, the preview or the server changed what a seat looks like: a new `look`.
  useEffect(() => {
    recolor(nodes.current, props.look);
  }, [props.look]);

  // The card lives in the viewport; created once the viewport exists. Its stylesheet is a normal
  // file of this app (the page allows only same-origin styles), so it is not injected.
  useEffect(() => {
    const vp = viewport.current;
    if (vp === null) return;
    const c = createSeatCard(vp, { injectStyles: false });
    card.current = c;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") hideCard();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      c.destroy();
      card.current = null;
    };
  }, [hideCard]);

  // What the tray, the preview or the server changed is also what the open card should say.
  useEffect(() => {
    if (shownKey.current !== null) showCardFor(shownKey.current);
  }, [props.look, props.cardOf, showCardFor]);

  // The stage marker moved.
  useEffect(() => {
    if (inner.current !== null) drawStage(inner.current, props.stage, view.current.scale);
  }, [props.stage]);

  // Keep the whole hall in view while the person has not moved the map.
  useEffect(() => {
    const vp = viewport.current;
    if (vp === null || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      if (!moved.current) fit();
    });
    ro.observe(vp);
    return () => ro.disconnect();
  }, [fit]);

  // Wheel zoom needs a non-passive listener to stop the page from scrolling.
  useEffect(() => {
    const vp = viewport.current;
    if (vp === null) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = vp.getBoundingClientRect();
      view.current = wheelZoom(view.current, e.deltaY, e.clientX - r.left, e.clientY - r.top);
      moved.current = true;
      apply();
    };
    vp.addEventListener("wheel", onWheel, { passive: false });
    return () => vp.removeEventListener("wheel", onWheel);
  }, [apply]);

  // ── gestures ──────────────────────────────────────────────────────────
  const pointers = useRef(new Map<number, Point>());
  const g = useRef<{
    mode: Mode;
    start: Point;
    last: Point;
    row: string | null;
    lastDist: number;
    lastMid: Point;
    lasso: Point[];
    rectStart: Point;
    additive: boolean;
  }>({
    mode: "none",
    start: { x: 0, y: 0 },
    last: { x: 0, y: 0 },
    row: null,
    lastDist: 0,
    lastMid: { x: 0, y: 0 },
    lasso: [],
    rectStart: { x: 0, y: 0 },
    additive: false,
  });

  const local = (e: { clientX: number; clientY: number }): Point => {
    const r = viewport.current!.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };
  const toCanvas = (p: Point): Point => ({
    x: (p.x - view.current.x) / view.current.scale,
    y: (p.y - view.current.y) / view.current.scale,
  });

  // ── the card: hover (mouse) and long press (touch) ────────────────────
  const hoverFrame = useRef<{ cancel: () => void } | null>(null);
  const hoverAt = useRef<Point | null>(null);
  const pressTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const cancelHover = () => {
    hoverFrame.current?.cancel();
    hoverFrame.current = null;
  };
  const clearPress = () => {
    if (pressTimer.current !== null) clearTimeout(pressTimer.current);
    pressTimer.current = null;
  };
  useEffect(
    () => () => {
      hoverFrame.current?.cancel();
      if (pressTimer.current !== null) clearTimeout(pressTimer.current);
    },
    [],
  );

  /** A mouse over the map: one hit test per animation frame, at the last position. */
  const scheduleHover = (e: { clientX: number; clientY: number }) => {
    hoverAt.current = { x: e.clientX, y: e.clientY };
    if (hoverFrame.current !== null) return;
    const run = () => {
      hoverFrame.current = null;
      const at = hoverAt.current;
      const vp = viewport.current;
      if (at === null || vp === null) return;
      const r = vp.getBoundingClientRect();
      const s = seatAt(latest.current.index, toCanvas({ x: at.x - r.left, y: at.y - r.top }), 0);
      if (s === null) hideCard();
      else if (s.key !== shownKey.current) showCardFor(s.key);
    };
    if (typeof requestAnimationFrame === "function") {
      const id = requestAnimationFrame(run);
      hoverFrame.current = { cancel: () => cancelAnimationFrame(id) };
    } else {
      const id = setTimeout(run, 16);
      hoverFrame.current = { cancel: () => clearTimeout(id) };
    }
  };

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    const vp = viewport.current;
    if (vp === null) return;
    if (e.pointerType === "mouse" && e.button !== 0) return;
    // A gesture starts: the card steps aside.
    hideCard();
    cancelHover();
    clearPress();
    try {
      vp.setPointerCapture(e.pointerId);
    } catch {
      // A synthetic event has no active pointer: the gesture still works without capture.
    }
    const p = local(e);
    pointers.current.set(e.pointerId, p);
    const st = g.current;
    if (pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()] as [Point, Point];
      st.mode = "pinch";
      st.lastDist = pointerDist(a, b);
      st.lastMid = pointerMid(a, b);
      if (inner.current !== null) drawShape(inner.current, null, view.current.scale);
      return;
    }
    if (pointers.current.size > 2) return;
    st.start = p;
    st.last = p;
    st.additive = e.shiftKey || e.ctrlKey || e.metaKey;
    const handle = (e.target as Element).closest?.("[data-row]");
    st.row = handle?.getAttribute("data-row") ?? null;
    const t = latest.current.tool;
    if ((t === "rect" || t === "lasso") && st.row === null) {
      st.mode = "draw";
      st.rectStart = toCanvas(p);
      st.lasso = [st.rectStart];
    } else {
      st.mode = "tap";
      // Touch has no hover: a finger that stays on a seat shows the card and does not paint.
      if (e.pointerType !== "mouse" && st.row === null) {
        pressTimer.current = setTimeout(() => {
          pressTimer.current = null;
          const now = g.current;
          if (now.mode !== "tap" || pointers.current.size !== 1) return;
          const seat = seatAt(latest.current.index, toCanvas(now.start), SEAT_SLACK_PX / view.current.scale);
          if (seat === null) return;
          now.mode = "peek";
          showCardFor(seat.key);
        }, LONG_PRESS_MS);
      }
    }
  };

  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!pointers.current.has(e.pointerId)) {
      // No button down: a mouse is only hovering. It informs, it never acts.
      if (e.pointerType === "mouse" && e.buttons === 0) scheduleHover(e);
      return;
    }
    const p = local(e);
    pointers.current.set(e.pointerId, p);
    const st = g.current;
    const el = inner.current;
    if (st.mode === "pinch" && pointers.current.size >= 2) {
      const [a, b] = [...pointers.current.values()] as [Point, Point];
      const dist = pointerDist(a, b);
      const mid = pointerMid(a, b);
      let next = pinchZoom(view.current, st.lastDist, dist, mid.x, mid.y);
      next = pan(next, mid.x - st.lastMid.x, mid.y - st.lastMid.y);
      view.current = next;
      st.lastDist = dist;
      st.lastMid = mid;
      moved.current = true;
      apply();
      return;
    }
    if (st.mode === "tap" || st.mode === "peek") {
      if (Math.hypot(p.x - st.start.x, p.y - st.start.y) > TAP_SLOP) {
        st.mode = "pan";
        clearPress();
      }
    }
    if (st.mode === "pan") {
      view.current = pan(view.current, p.x - st.last.x, p.y - st.last.y);
      st.last = p;
      moved.current = true;
      apply();
      return;
    }
    if (st.mode === "draw" && el !== null) {
      const c = toCanvas(p);
      if (latest.current.tool === "rect") {
        const r = normalizeRect(st.rectStart, c);
        drawShape(el, { kind: "rect", ...r }, view.current.scale);
      } else {
        const lastPoint = st.lasso[st.lasso.length - 1]!;
        if (Math.hypot(c.x - lastPoint.x, c.y - lastPoint.y) * view.current.scale > 3) st.lasso.push(c);
        drawShape(el, { kind: "lasso", points: st.lasso }, view.current.scale);
      }
    }
  };

  const finish = (e: React.PointerEvent<HTMLDivElement>, cancelled: boolean) => {
    finishGesture(e, cancelled);
    // The mouse stays where it was lifted: the card follows it again.
    if (e.pointerType === "mouse" && !cancelled && pointers.current.size === 0) scheduleHover(e);
  };

  const finishGesture = (e: React.PointerEvent<HTMLDivElement>, cancelled: boolean) => {
    const st = g.current;
    const had = pointers.current.has(e.pointerId);
    pointers.current.delete(e.pointerId);
    clearPress();
    try {
      viewport.current?.releasePointerCapture(e.pointerId);
    } catch {
      // Already released.
    }
    if (!had) return;
    const el = inner.current;
    if (st.mode === "pinch") {
      if (pointers.current.size === 1) {
        // One finger is left: it moves the map, without a jump.
        st.mode = "pan";
        st.last = [...pointers.current.values()][0]!;
      } else if (pointers.current.size === 0) st.mode = "none";
      return;
    }
    if (st.mode === "peek") {
      // The long press only showed the card: lifting the finger paints nothing.
      st.mode = "none";
      return;
    }
    const p = local(e);
    const { index: idx, tool, onSeats, onRowHandle, onStage } = latest.current;
    if (st.mode === "draw") {
      if (el !== null) drawShape(el, null, view.current.scale);
      st.mode = "none";
      if (cancelled) return;
      const c = toCanvas(p);
      let keys: string[] = [];
      if (tool === "rect") {
        const tiny = Math.hypot(p.x - st.start.x, p.y - st.start.y) < TAP_SLOP;
        if (tiny) {
          const s = seatAt(idx, c, SEAT_SLACK_PX / view.current.scale);
          keys = s === null ? [] : [s.key];
        } else keys = seatsInRect(idx, normalizeRect(st.rectStart, c));
      } else {
        keys = seatsInPolygon(idx, st.lasso);
      }
      if (keys.length > 0) onSeats(keys, tool);
      return;
    }
    if (st.mode === "tap" && !cancelled) {
      st.mode = "none";
      if (st.row !== null && tool !== "stage") {
        onRowHandle(st.row, st.additive);
        return;
      }
      const c = toCanvas(p);
      if (tool === "stage") {
        onStage(c);
        return;
      }
      if (tool === "pan" || tool === "rect" || tool === "lasso") return;
      const s = seatAt(idx, c, SEAT_SLACK_PX / view.current.scale);
      if (s === null) return;
      if (tool === "seat") onSeats([s.key], tool);
      else if (tool === "row") onSeats([...rowSeatsOf(idx, s.key)], tool);
      else if (tool === "section") onSeats([...sectionSeatsOf(idx, s.key)], tool);
      return;
    }
    st.mode = "none";
  };

  return (
    <div
      ref={viewport}
      className="sm-viewport"
      data-testid="seat-map"
      data-tool={props.tool}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={(e) => finish(e, false)}
      onPointerCancel={(e) => finish(e, true)}
      onPointerLeave={(e) => {
        // A finger leaves the map right after it lifts: only a mouse leaving puts the card away.
        if (e.pointerType !== "mouse") return;
        cancelHover();
        hideCard();
      }}
    >
      <div ref={inner} className="sm-inner" dangerouslySetInnerHTML={{ __html: svg }} />
    </div>
  );
});
