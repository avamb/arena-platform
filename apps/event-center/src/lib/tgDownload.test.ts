/** How a PDF reaches the person in the mini app (lib/tgDownload.ts). */
import { describe, expect, it, vi } from "vitest";
import { deliverPdf, type PdfDeps } from "@/lib/tgDownload";

const file = { blob: new Blob(["%PDF-1.4"], { type: "application/pdf" }), filename: "invitation-1000000500.pdf" };

function deps(over: Partial<PdfDeps> = {}) {
  const share = vi.fn<(f: File) => Promise<void>>(async () => undefined);
  const save = vi.fn<(f: typeof file) => void>();
  const all: PdfDeps = { inTelegram: () => true, canShare: () => true, share, save, ...over };
  return { all, share: over.share ? (over.share as typeof share) : share, save };
}

const failWith = (name: string) => async (): Promise<void> => {
  throw new DOMException("x", name);
};

describe("deliverPdf", () => {
  it("offers the file to the share sheet inside Telegram, named and typed as the PDF", async () => {
    const d = deps();
    expect(await deliverPdf(file, d.all)).toBe("shared");
    const sent = d.share.mock.calls[0][0];
    expect(sent.name).toBe("invitation-1000000500.pdf");
    expect(sent.type).toBe("application/pdf");
    expect(d.save).not.toHaveBeenCalled();
  });

  it("treats a closed share sheet as done, not as a failure to fall back from", async () => {
    const d = deps({ share: failWith("AbortError") });
    expect(await deliverPdf(file, d.all)).toBe("shared");
    expect(d.save).not.toHaveBeenCalled();
  });

  it("downloads the file when the window cannot share it", async () => {
    const d = deps({ canShare: () => false });
    expect(await deliverPdf(file, d.all)).toBe("saved");
    expect(d.save).toHaveBeenCalledWith(file);
    expect(d.share).not.toHaveBeenCalled();
  });

  it("downloads the file when the share sheet is refused", async () => {
    const d = deps({ share: failWith("NotAllowedError") });
    expect(await deliverPdf(file, d.all)).toBe("saved");
    expect(d.save).toHaveBeenCalledWith(file);
  });

  it("is the plain download outside Telegram and never asks the share sheet", async () => {
    const d = deps({ inTelegram: () => false });
    expect(await deliverPdf(file, d.all)).toBe("saved");
    expect(d.share).not.toHaveBeenCalled();
    expect(d.save).toHaveBeenCalledWith(file);
  });
});
