/**
 * Unit tests for the pure validation helpers exported by
 * <ImageUpload />. The React surface itself is not exercised here
 * because the admin-web test environment runs in Node with no
 * jsdom -- file pickers, FileReader, Image, and Object URLs all live
 * in the browser. Coverage of the rendered control comes from the
 * host pages that mount the component.
 */
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import {
  ACCEPTED_MIME_TYPES,
  MAX_SVG_UPLOAD_BYTES,
  MAX_UPLOAD_BYTES,
  OWNER_TYPE_CONSTRAINTS,
  OWNER_TYPE_MAX_BYTES,
  OWNER_TYPE_MIME_TYPES,
  SVG_MIME_TYPE,
  acceptedExtensionsLabel,
  formatBytes,
  formatUploadProgress,
  validateDimensions,
  validateFile,
} from "@/components/ImageUpload";

describe("ImageUpload constraints", () => {
  it("exposes the canonical accepted MIME list", () => {
    expect(ACCEPTED_MIME_TYPES).toEqual(["image/jpeg", "image/png", "image/webp"]);
  });

  it("caps uploads at 5 MiB", () => {
    expect(MAX_UPLOAD_BYTES).toBe(5 * 1024 * 1024);
  });

  it("requires 600x400 minimum for event posters and no minimum for logos/photos", () => {
    expect(OWNER_TYPE_CONSTRAINTS.event_poster.minWidth).toBe(600);
    expect(OWNER_TYPE_CONSTRAINTS.event_poster.minHeight).toBe(400);
    expect(OWNER_TYPE_CONSTRAINTS.org_logo.minWidth).toBeNull();
    expect(OWNER_TYPE_CONSTRAINTS.org_logo.minHeight).toBeNull();
    expect(OWNER_TYPE_CONSTRAINTS.artist_photo.minWidth).toBeNull();
    expect(OWNER_TYPE_CONSTRAINTS.artist_photo.minHeight).toBeNull();
  });

  it("imposes no pixel minimum on vector seating plans", () => {
    // A dimension probe decodes the file into an <img>; SVGs have no
    // intrinsic raster size, so the component must skip that path entirely.
    expect(OWNER_TYPE_CONSTRAINTS.seating_plan_svg.minWidth).toBeNull();
    expect(OWNER_TYPE_CONSTRAINTS.seating_plan_svg.minHeight).toBeNull();
  });
});

describe("validateFile", () => {
  it("rejects empty files", () => {
    const v = validateFile({ type: "image/png", size: 0 }, "org_logo");
    expect(v?.code).toBe("empty");
  });

  it("rejects unsupported types", () => {
    const v = validateFile({ type: "image/gif", size: 1024 }, "org_logo");
    expect(v?.code).toBe("type");
    expect(v?.message).toContain("image/gif");
  });

  it("rejects oversized files", () => {
    const v = validateFile(
      { type: "image/jpeg", size: MAX_UPLOAD_BYTES + 1 },
      "event_poster",
    );
    expect(v?.code).toBe("size");
  });

  it.each(ACCEPTED_MIME_TYPES)("accepts %s within the size cap", (mime) => {
    expect(validateFile({ type: mime, size: 1024 }, "org_logo")).toBeNull();
    expect(
      validateFile({ type: mime, size: MAX_UPLOAD_BYTES }, "event_poster"),
    ).toBeNull();
  });

  it("treats a missing MIME string as unsupported", () => {
    const v = validateFile({ type: "", size: 1024 }, "org_logo");
    expect(v?.code).toBe("type");
    expect(v?.message).toContain("(unknown)");
  });
});

describe("validateFile for seating_plan_svg (AB-25b)", () => {
  it("accepts an SVG document within the vector size cap", () => {
    expect(
      validateFile({ type: SVG_MIME_TYPE, size: 4096 }, "seating_plan_svg"),
    ).toBeNull();
    expect(
      validateFile(
        { type: SVG_MIME_TYPE, size: MAX_SVG_UPLOAD_BYTES },
        "seating_plan_svg",
      ),
    ).toBeNull();
  });

  it("rejects raster images for a seating plan", () => {
    const v = validateFile({ type: "image/png", size: 1024 }, "seating_plan_svg");
    expect(v?.code).toBe("type");
    expect(v?.message).toContain("image/png");
    expect(v?.message).toContain("svg");
  });

  it("rejects SVG uploads over the 2 MiB vector cap", () => {
    const v = validateFile(
      { type: SVG_MIME_TYPE, size: MAX_SVG_UPLOAD_BYTES + 1 },
      "seating_plan_svg",
    );
    expect(v?.code).toBe("size");
    expect(v?.message).toContain("2.00 MiB");
  });

  it("still rejects SVG uploads for raster owner types", () => {
    const v = validateFile({ type: SVG_MIME_TYPE, size: 1024 }, "org_logo");
    expect(v?.code).toBe("type");
  });

  it("keeps the vector cap under the backend version-create body limit", () => {
    // hseating/versions.go caps the version-create JSON body at 4 MiB and the
    // same document travels inline in that request.
    expect(MAX_SVG_UPLOAD_BYTES).toBeLessThan(4 * 1024 * 1024);
    expect(OWNER_TYPE_MAX_BYTES.seating_plan_svg).toBe(MAX_SVG_UPLOAD_BYTES);
    expect(OWNER_TYPE_MAX_BYTES.org_logo).toBe(MAX_UPLOAD_BYTES);
  });

  it("exposes the per-owner-type MIME allowlist", () => {
    expect(OWNER_TYPE_MIME_TYPES.seating_plan_svg).toEqual([SVG_MIME_TYPE]);
    expect(OWNER_TYPE_MIME_TYPES.org_logo).toEqual(ACCEPTED_MIME_TYPES);
  });
});

describe("acceptedExtensionsLabel", () => {
  it("renders the raster list with the jpeg alias normalised", () => {
    expect(acceptedExtensionsLabel("org_logo")).toBe("jpg, png, webp");
  });

  it("renders svg for seating plans", () => {
    expect(acceptedExtensionsLabel("seating_plan_svg")).toBe("svg");
  });
});

describe("validateDimensions", () => {
  it("returns null for owner_types without dimension requirements", () => {
    expect(validateDimensions(10, 10, "org_logo")).toBeNull();
    expect(validateDimensions(10, 10, "artist_photo")).toBeNull();
  });

  it("rejects posters under 600x400", () => {
    const v = validateDimensions(599, 400, "event_poster");
    expect(v?.code).toBe("dimensions");
    expect(v?.message).toContain("599x400");
    expect(v?.message).toContain("600x400");
  });

  it("rejects posters with insufficient height", () => {
    expect(validateDimensions(600, 399, "event_poster")?.code).toBe(
      "dimensions",
    );
  });

  it("accepts posters at exactly the minimum and above", () => {
    expect(validateDimensions(600, 400, "event_poster")).toBeNull();
    expect(validateDimensions(1200, 800, "event_poster")).toBeNull();
  });
});

describe("formatBytes", () => {
  it("formats byte counts with appropriate units", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(2048)).toBe("2.0 KiB");
    expect(formatBytes(MAX_UPLOAD_BYTES)).toBe("5.00 MiB");
  });
});

describe("formatUploadProgress (M-6)", () => {
  it("renders a Starting label when nothing has been measured yet", () => {
    expect(formatUploadProgress(null)).toBe("Starting…");
  });

  it("renders an indeterminate label when total is unknown", () => {
    expect(
      formatUploadProgress({ loaded: 1024, total: 0, fraction: 0 }),
    ).toBe("Uploading… 1.0 KiB");
  });

  it("renders a percentage when total is known", () => {
    expect(
      formatUploadProgress({
        loaded: 512 * 1024,
        total: 1024 * 1024,
        fraction: 0.5,
      }),
    ).toBe("Uploading… 50% (512.0 KiB / 1.00 MiB)");
  });

  it("clamps the percentage to 0..100", () => {
    expect(
      formatUploadProgress({ loaded: -1, total: 1, fraction: -0.5 }),
    ).toContain("0%");
    expect(
      formatUploadProgress({ loaded: 10, total: 1, fraction: 10 }),
    ).toContain("100%");
  });
});

describe("ImageUpload mobile contract (M-6)", () => {
  const source = readFileSync(
    resolve(__dirname, "ImageUpload.tsx"),
    "utf8",
  );

  it("uses accept=image/* so mobile browsers offer the camera", () => {
    // The narrower jpg/png/webp gate is enforced by validateFile, so
    // the input attribute itself must be the wildcard form.
    expect(source).toMatch(/accept="image\/\*"/);
  });

  it("sets capture=environment to hint the rear-facing camera", () => {
    expect(source).toMatch(/capture="environment"/);
  });

  it("renders a cancel button while an upload is in flight", () => {
    expect(source).toMatch(/data-testid=\{`\$\{testIdPrefix\}-cancel`\}/);
    expect(source).toMatch(/aria-label="Cancel upload"/);
  });

  it("renders a progressbar with aria-valuenow during uploads", () => {
    expect(source).toMatch(/role="progressbar"/);
    expect(source).toMatch(/aria-valuemin=\{0\}/);
    expect(source).toMatch(/aria-valuemax=\{100\}/);
    expect(source).toMatch(/data-testid=\{`\$\{testIdPrefix\}-progress`\}/);
  });

  it("wires the upload mutation to the cancellable AbortController path", () => {
    expect(source).toMatch(/abortRef\.current/);
    expect(source).toMatch(/onProgress:/);
    expect(source).toMatch(/signal:\s*controller\.signal/);
  });

  it("still enforces the 5 MiB client-side cap before POST", () => {
    expect(source).toMatch(/validateFile\(file, ownerType\)/);
  });
});
