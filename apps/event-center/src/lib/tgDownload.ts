/**
 * How a downloaded PDF reaches the person (spec 46, 3.4 and 4.2, INV-05m).
 *
 * The spec wants «Download» in the mini app to go through
 * `Telegram.WebApp.downloadFile` (Bot API 8.0). That call takes a URL and the
 * TELEGRAM CLIENT fetches it, outside the window: it cannot send our
 * `Authorization` header, and the PDF route
 * (`GET .../invitations/{order_id}/tickets.pdf`) is authenticated with it.
 * There is no short-lived download path yet, so `downloadFile` cannot be used
 * (the stop condition of INV-05: «downloadFile does not work → decide the
 * fallback»). The fallback is what the window itself can do with the bytes it
 * already fetched with its own session:
 *
 *   1. the system share sheet with the file (Web Share API level 2), where the
 *      webview has it: from there the person sends the PDF to the guest;
 *   2. a normal download (an object URL on an <a download>), as the web screen
 *      does;
 *   3. in a Telegram window the page also says where the sure way is: the same
 *      screen in the phone's browser.
 *
 * When the server gets a download path that needs no header, the Telegram
 * branch is the one place to call `downloadFile(url, file_name)` instead.
 */
import type { DownloadedFile } from "@/lib/api/http";
import { insideTelegram } from "@/lib/tgEdit";

/** "shared" went through the share sheet. "saved" was handed to the browser's download. */
export type PdfDelivery = "shared" | "saved";

export interface PdfDeps {
  inTelegram: () => boolean;
  canShare: (file: File) => boolean;
  share: (file: File) => Promise<void>;
  save: (file: DownloadedFile) => void;
}

/** Hands a downloaded file to the browser. */
export function saveBlob(file: DownloadedFile): void {
  const url = URL.createObjectURL(file.blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = file.filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export const browserPdfDeps: PdfDeps = {
  inTelegram: () => insideTelegram(),
  canShare: (file) => typeof navigator.canShare === "function" && navigator.canShare({ files: [file] }),
  share: (file) => navigator.share({ files: [file], title: file.name }),
  save: saveBlob,
};

function isAbort(err: unknown): boolean {
  return typeof err === "object" && err !== null && (err as { name?: string }).name === "AbortError";
}

/**
 * Delivers the PDF. Inside Telegram it tries the share sheet first; a person
 * who closes the sheet is not an error (nothing more happens), any other
 * refusal falls back to a plain download. Outside Telegram it is the plain
 * download of the web screen.
 */
export async function deliverPdf(file: DownloadedFile, deps: PdfDeps = browserPdfDeps): Promise<PdfDelivery> {
  if (deps.inTelegram()) {
    const pdf = new File([file.blob], file.filename, { type: "application/pdf" });
    if (deps.canShare(pdf)) {
      try {
        await deps.share(pdf);
        return "shared";
      } catch (err) {
        if (isAbort(err)) return "shared";
        // Not allowed here (no fresh tap, a webview without it): the plain download below.
      }
    }
  }
  deps.save(file);
  return "saved";
}
