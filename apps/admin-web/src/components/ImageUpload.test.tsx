/**
 * Unit tests for the pure validation helpers exported by
 * <ImageUpload />. The React surface itself is not exercised here
 * because the admin-web test environment runs in Node with no
 * jsdom -- file pickers, FileReader, Image, and Object URLs all live
 * in the browser. Coverage of the rendered control comes from the
 * host pages that mount the component.
 */
import { describe, it, expect } from "vitest";
import {
  ACCEPTED_MIME_TYPES,
  MAX_UPLOAD_BYTES,
  OWNER_TYPE_CONSTRAINTS,
  formatBytes,
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
