/**
 * Reads a file with the bearer token and gives it back as a blob URL, for an
 * <img src>. The draft SVG and the source files have no public link, so an
 * <img> cannot fetch them by itself, and the markup is never put into the page
 * as HTML. The URL is revoked when the component leaves or the key changes.
 */
import { useEffect, useState } from "react";

export interface BlobUrl {
  url: string | null;
  failed: boolean;
}

/** `load` is read once per `key`. A null `load` reads nothing. */
export function useBlobUrl(key: string, load: (() => Promise<Blob>) | null): BlobUrl {
  const [state, setState] = useState<BlobUrl>({ url: null, failed: false });
  useEffect(() => {
    setState({ url: null, failed: false });
    if (load === null) return;
    let alive = true;
    let made: string | null = null;
    load()
      .then((blob) => {
        if (!alive) return;
        made = URL.createObjectURL(blob);
        setState({ url: made, failed: false });
      })
      .catch(() => {
        if (alive) setState({ url: null, failed: true });
      });
    return () => {
      alive = false;
      if (made !== null) URL.revokeObjectURL(made);
    };
    // `load` is a new function on every render: the key says when to read again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  return state;
}

/** An SVG answer keeps its type, so the browser draws it as an image and runs no script. */
export function asSvg(blob: Blob): Blob {
  return blob.type === "image/svg+xml" ? blob : new Blob([blob], { type: "image/svg+xml" });
}
