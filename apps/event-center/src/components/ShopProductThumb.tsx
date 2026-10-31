/**
 * The small picture of a product in the list (spec 47, section 6): the first picture, asked from the
 * media service at 240 px wide (`w=240`), drawn about 48 px. It is fetched only when the row comes into
 * view, so a list of a hundred products does not start a hundred downloads at once.
 *
 * Without `media.read` (or without a picture) there is nothing to draw and the cell shows only the count.
 */
import { useEffect, useRef, useState } from "react";
import { usePosterImage } from "@/components/EventPoster";

/** The width asked of the media service: five times the drawn size, sharp on a dense screen. */
export const SHOP_LIST_THUMB_WIDTH = 240;

/** True once the element has been near the viewport (always true where the browser cannot tell). */
function useSeen(): [React.RefObject<HTMLSpanElement>, boolean] {
  const ref = useRef<HTMLSpanElement>(null);
  const [seen, setSeen] = useState(typeof IntersectionObserver === "undefined");
  useEffect(() => {
    if (seen || ref.current === null || typeof IntersectionObserver === "undefined") return;
    const node = ref.current;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setSeen(true);
          observer.disconnect();
        }
      },
      { rootMargin: "200px" },
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, [seen]);
  return [ref, seen];
}

export function ShopProductThumb({ mediaId, canView, alt }: { mediaId: string; canView: boolean; alt: string }) {
  const [ref, seen] = useSeen();
  const image = usePosterImage(mediaId, canView && seen, SHOP_LIST_THUMB_WIDTH);
  return (
    <span ref={ref} className="shop-thumb-box" data-testid="shop-product-thumb">
      {image.state.phase === "ok" ? (
        <img className="shop-thumb" src={image.state.url} alt={alt} loading="lazy" decoding="async" width={48} height={48} />
      ) : (
        <span className="shop-thumb shop-thumb-empty" aria-hidden="true" />
      )}
    </span>
  );
}
