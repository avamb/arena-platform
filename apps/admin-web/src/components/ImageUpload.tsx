/**
 * <ImageUpload /> — reusable image-upload control for admin surfaces
 * that own a `*_media_id` foreign key (Wave G feature #287 / G-3).
 *
 * Two-step flow per the platform contract:
 *
 *   1. The operator picks a file. The component validates the file
 *      *client-side* (MIME type, byte size, and optional minimum
 *      dimensions) and renders a local preview from an Object URL.
 *   2. The component POSTs the file to `/v1/media`, then -- if the
 *      caller supplied a `patch` descriptor -- PATCHes the owning
 *      entity with `{ [field]: media_id }`. The new `media_id` is
 *      also reported through the optional `onChange` callback so a
 *      parent form can collect it for a deferred save.
 *
 * Owner-type table:
 *
 *   org_logo       jpg / png / webp, <= 5 MiB, no min dimensions
 *   event_poster   jpg / png / webp, <= 5 MiB, min 600 x 400
 *   artist_photo   jpg / png / webp, <= 5 MiB, no min dimensions
 *
 * The constraints are intentionally enforced in the browser AND in
 * the backend (`apps/backend/internal/adapters/storage`): the
 * client-side checks are a UX nicety so the operator learns about a
 * bad file before the bytes leave their machine, but the server is
 * the source of truth.
 *
 * Accessibility:
 *   - The hidden <input type=file> is reachable via a labelled
 *     button (`Replace` / `Choose image`).
 *   - Validation failures render in a `role=alert` region so screen
 *     readers announce them.
 *   - The remove button has an explicit `aria-label` so its purpose
 *     is unambiguous when no label text is rendered.
 *
 * Tests: pure validation helpers live alongside the component (see
 * `validateFile` / `OWNER_TYPE_CONSTRAINTS`). The React surface is
 * deliberately thin because the admin-web test environment is
 * Node-only (no jsdom); rendering is exercised by the host pages.
 */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  authedFetch,
  fetchMediaObject,
  uploadMedia,
  type MediaObjectWithUrl,
  type MediaOwnerType,
  type UploadProgress,
} from "@/lib/api/client";

// ---------------------------------------------------------------------------
// Public constraints
// ---------------------------------------------------------------------------

export const ACCEPTED_MIME_TYPES = [
  "image/jpeg",
  "image/png",
  "image/webp",
] as const;
export type AcceptedMimeType = (typeof ACCEPTED_MIME_TYPES)[number];

/** Hard ceiling on the uploaded file size: 5 MiB. */
export const MAX_UPLOAD_BYTES = 5 * 1024 * 1024;

export interface OwnerTypeConstraint {
  readonly minWidth: number | null;
  readonly minHeight: number | null;
  readonly label: string;
}

/**
 * Per-owner-type constraints. The set lives at module scope so tests
 * (and host pages that want to render a hint) can read it without
 * re-instantiating the component.
 */
export const OWNER_TYPE_CONSTRAINTS: Record<MediaOwnerType, OwnerTypeConstraint> = {
  org_logo: { minWidth: null, minHeight: null, label: "Organization logo" },
  event_poster: { minWidth: 600, minHeight: 400, label: "Event poster" },
  artist_photo: { minWidth: null, minHeight: null, label: "Artist photo" },
};

// ---------------------------------------------------------------------------
// Validation helpers (exported for unit tests)
// ---------------------------------------------------------------------------

export type ValidationCode =
  | "type"
  | "size"
  | "dimensions"
  | "empty";

export interface ValidationFailure {
  readonly code: ValidationCode;
  readonly message: string;
}

/**
 * Validates the MIME type and byte size of a candidate upload.
 * Dimension validation requires an actual decoded image, so it lives
 * in `validateDimensions` and is called only after the browser has
 * loaded the file into an `Image` element.
 */
export function validateFile(
  file: { type: string; size: number },
  ownerType: MediaOwnerType,
): ValidationFailure | null {
  if (file.size === 0) {
    return {
      code: "empty",
      message: "File is empty.",
    };
  }
  if (
    !ACCEPTED_MIME_TYPES.includes(file.type as AcceptedMimeType)
  ) {
    return {
      code: "type",
      message: `Unsupported file type ${file.type || "(unknown)"}. Allowed: jpg, png, webp.`,
    };
  }
  if (file.size > MAX_UPLOAD_BYTES) {
    return {
      code: "size",
      message: `File is ${formatBytes(file.size)}; maximum is ${formatBytes(MAX_UPLOAD_BYTES)}.`,
    };
  }
  void ownerType; // size + type are uniform across owner_types today.
  return null;
}

/**
 * Validates decoded image dimensions against the per-owner-type
 * minimum. Returns null when the constraint is satisfied or when the
 * owner_type has no dimensional requirement.
 */
export function validateDimensions(
  width: number,
  height: number,
  ownerType: MediaOwnerType,
): ValidationFailure | null {
  const c = OWNER_TYPE_CONSTRAINTS[ownerType];
  if (c.minWidth === null || c.minHeight === null) {
    return null;
  }
  if (width < c.minWidth || height < c.minHeight) {
    return {
      code: "dimensions",
      message: `Image is ${width}x${height}; minimum is ${c.minWidth}x${c.minHeight}.`,
    };
  }
  return null;
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / (1024 * 1024)).toFixed(2)} MiB`;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export interface ImageUploadPatchTarget {
  /** Owning entity PATCH path, e.g. `/v1/organizations/{org_id}`. */
  readonly path: string;
  /** Body field name to set, e.g. `logo_media_id`. */
  readonly field: string;
  /** React-query keys to invalidate after a successful PATCH. */
  readonly invalidateQueryKeys?: ReadonlyArray<ReadonlyArray<unknown>>;
}

export interface ImageUploadProps {
  readonly ownerType: MediaOwnerType;
  /** Owning organization. Required for `event_poster` and `artist_photo`. */
  readonly orgId?: string | null;
  /** Owning entity row ID (e.g. event_id). */
  readonly ownerId?: string | null;
  /** Current persisted media_id, used to render the existing image. */
  readonly currentMediaId?: string | null;
  /** Fires whenever the persisted media_id changes (after upload or clear). */
  readonly onChange?: (mediaId: string | null) => void;
  /**
   * If supplied, the component PATCHes the owning entity itself after
   * a successful upload and again after a successful remove. Omit for
   * forms that batch all field saves into a single PATCH triggered by
   * their own submit button.
   */
  readonly patch?: ImageUploadPatchTarget;
  readonly disabled?: boolean;
  /** Prefix for `data-testid` on the rendered controls. */
  readonly testIdPrefix?: string;
}

export function ImageUpload({
  ownerType,
  orgId,
  ownerId,
  currentMediaId,
  onChange,
  patch,
  disabled = false,
  testIdPrefix = "image-upload",
}: ImageUploadProps): JSX.Element {
  const queryClient = useQueryClient();
  const inputRef = useRef<HTMLInputElement>(null);
  const [activeMediaId, setActiveMediaId] = useState<string | null>(
    currentMediaId ?? null,
  );
  const [pendingPreview, setPendingPreview] = useState<string | null>(null);
  const [validationError, setValidationError] = useState<ValidationFailure | null>(
    null,
  );
  const [serverError, setServerError] = useState<string | null>(null);
  const [uploadProgress, setUploadProgress] = useState<UploadProgress | null>(
    null,
  );
  const abortRef = useRef<AbortController | null>(null);

  // Sync external currentMediaId -> internal active id when the parent
  // refetches (e.g. after a parent-driven PATCH succeeds).
  useEffect(() => {
    setActiveMediaId(currentMediaId ?? null);
  }, [currentMediaId]);

  // Object-URL lifecycle for the in-progress local preview.
  useEffect(() => {
    if (pendingPreview === null) return;
    return () => {
      URL.revokeObjectURL(pendingPreview);
    };
  }, [pendingPreview]);

  // Fetch the persisted image's signed URL for preview. Only enabled
  // when we have a persisted id and no fresher local preview.
  const previewQuery = useQuery<MediaObjectWithUrl, ApiError>({
    queryKey: ["media", activeMediaId],
    queryFn: () => {
      if (activeMediaId === null) {
        return Promise.reject(new Error("no media id"));
      }
      return fetchMediaObject(activeMediaId);
    },
    enabled: activeMediaId !== null && pendingPreview === null,
    retry: (count, err) =>
      err instanceof ApiError && (err.status === 401 || err.status === 403 || err.status === 404)
        ? false
        : count < 2,
    refetchOnWindowFocus: false,
    // Signed URLs are short-lived; we keep them fresh by refetching
    // every 4 minutes while the component is mounted.
    staleTime: 4 * 60 * 1000,
  });

  const uploadMutation = useMutation<
    string,
    ApiError | Error,
    { file: File }
  >({
    mutationFn: async ({ file }) => {
      const controller = new AbortController();
      abortRef.current = controller;
      setUploadProgress({ loaded: 0, total: file.size, fraction: 0 });
      try {
        const obj = await uploadMedia({
          file,
          ownerType,
          orgId: orgId ?? null,
          ownerId: ownerId ?? null,
          onProgress: (p) => setUploadProgress(p),
          signal: controller.signal,
        });
        if (patch !== undefined) {
          await authedFetch({
            method: "PATCH",
            path: patch.path,
            body: { [patch.field]: obj.id },
          });
        }
        return obj.id;
      } finally {
        abortRef.current = null;
      }
    },
    onSuccess: (newId) => {
      setActiveMediaId(newId);
      setPendingPreview(null);
      setServerError(null);
      setUploadProgress(null);
      onChange?.(newId);
      if (patch?.invalidateQueryKeys !== undefined) {
        for (const key of patch.invalidateQueryKeys) {
          queryClient.invalidateQueries({ queryKey: [...key] });
        }
      }
    },
    onError: (err) => {
      setUploadProgress(null);
      // Aborted uploads are an intentional user action; surface a
      // gentle inline notice rather than the raw error envelope.
      if (err instanceof ApiError && err.code === "network.aborted") {
        setServerError("Upload cancelled.");
        setPendingPreview(null);
        return;
      }
      setServerError(formatApiError(err));
    },
  });

  const cancelUpload = useCallback((): void => {
    abortRef.current?.abort();
  }, []);

  const removeMutation = useMutation<void, ApiError | Error, void>({
    mutationFn: async () => {
      if (patch !== undefined) {
        await authedFetch({
          method: "PATCH",
          path: patch.path,
          body: { [patch.field]: null },
        });
      }
    },
    onSuccess: () => {
      setActiveMediaId(null);
      setPendingPreview(null);
      setServerError(null);
      onChange?.(null);
      if (patch?.invalidateQueryKeys !== undefined) {
        for (const key of patch.invalidateQueryKeys) {
          queryClient.invalidateQueries({ queryKey: [...key] });
        }
      }
    },
    onError: (err) => {
      setServerError(formatApiError(err));
    },
  });

  const constraint = OWNER_TYPE_CONSTRAINTS[ownerType];
  const busy = uploadMutation.isPending || removeMutation.isPending;
  const disabledNow = disabled || busy;

  const handleFile = useCallback(
    (file: File) => {
      setServerError(null);
      const failure = validateFile(file, ownerType);
      if (failure !== null) {
        setValidationError(failure);
        setPendingPreview(null);
        return;
      }
      // Local preview + dimension check via in-memory <img>. When
      // dimension constraints exist we await the natural size before
      // releasing the file to the upload mutation.
      const objectUrl = URL.createObjectURL(file);
      setPendingPreview(objectUrl);
      if (
        constraint.minWidth === null ||
        constraint.minHeight === null ||
        typeof Image === "undefined"
      ) {
        setValidationError(null);
        uploadMutation.mutate({ file });
        return;
      }
      const img = new Image();
      img.onload = () => {
        const dim = validateDimensions(
          img.naturalWidth,
          img.naturalHeight,
          ownerType,
        );
        if (dim !== null) {
          setValidationError(dim);
          URL.revokeObjectURL(objectUrl);
          setPendingPreview(null);
          return;
        }
        setValidationError(null);
        uploadMutation.mutate({ file });
      };
      img.onerror = () => {
        setValidationError({
          code: "type",
          message: "Could not decode image.",
        });
        URL.revokeObjectURL(objectUrl);
        setPendingPreview(null);
      };
      img.src = objectUrl;
    },
    [constraint.minHeight, constraint.minWidth, ownerType, uploadMutation],
  );

  const onPick = (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    if (f !== undefined) handleFile(f);
    // Allow re-picking the same file path.
    e.target.value = "";
  };

  const previewSrc = useMemo<string | null>(() => {
    if (pendingPreview !== null) return pendingPreview;
    if (previewQuery.data?.signed_url !== undefined) {
      return previewQuery.data.signed_url;
    }
    return null;
  }, [pendingPreview, previewQuery.data?.signed_url]);

  const hintBits: string[] = ["jpg, png, webp", `≤ ${formatBytes(MAX_UPLOAD_BYTES)}`];
  if (constraint.minWidth !== null && constraint.minHeight !== null) {
    hintBits.push(`min ${constraint.minWidth}×${constraint.minHeight}`);
  }
  const hint = hintBits.join(" · ");

  return (
    <div data-testid={testIdPrefix} style={containerStyle}>
      <div style={previewWrapStyle} data-testid={`${testIdPrefix}-preview`}>
        {previewSrc !== null ? (
          <img
            src={previewSrc}
            alt={`${constraint.label} preview`}
            style={previewImgStyle}
            data-testid={`${testIdPrefix}-preview-img`}
          />
        ) : (
          <div style={emptyPreviewStyle} aria-hidden>
            No image
          </div>
        )}
      </div>

      <div style={controlsStyle}>
        <button
          type="button"
          onClick={() => inputRef.current?.click()}
          disabled={disabledNow}
          style={primaryBtnStyle}
          data-testid={`${testIdPrefix}-pick`}
        >
          {uploadMutation.isPending
            ? "Uploading…"
            : activeMediaId !== null
              ? "Replace image"
              : "Choose image"}
        </button>
        {activeMediaId !== null ? (
          <button
            type="button"
            onClick={() => removeMutation.mutate()}
            disabled={disabledNow}
            style={secondaryBtnStyle}
            aria-label={`Remove ${constraint.label.toLowerCase()}`}
            data-testid={`${testIdPrefix}-remove`}
          >
            {removeMutation.isPending ? "Removing…" : "Remove"}
          </button>
        ) : null}
        {uploadMutation.isPending ? (
          <button
            type="button"
            onClick={cancelUpload}
            style={secondaryBtnStyle}
            aria-label="Cancel upload"
            data-testid={`${testIdPrefix}-cancel`}
          >
            Cancel
          </button>
        ) : null}
        <input
          ref={inputRef}
          type="file"
          // image/* is the M-6 mobile contract: iOS Safari and many
          // Android browsers only surface the camera option when the
          // wildcard image MIME type is present. The narrower
          // jpg/png/webp gate is still enforced by validateFile().
          accept="image/*"
          // `capture="environment"` is a hint to mobile browsers that
          // the rear-facing camera should be offered alongside the
          // photo library. Desktop browsers ignore it.
          capture="environment"
          onChange={onPick}
          disabled={disabledNow}
          style={hiddenInputStyle}
          data-testid={`${testIdPrefix}-input`}
        />
      </div>

      <p style={hintStyle} data-testid={`${testIdPrefix}-hint`}>
        {hint}
      </p>

      {uploadMutation.isPending ? (
        <div
          style={progressWrapStyle}
          data-testid={`${testIdPrefix}-progress`}
          aria-live="polite"
        >
          <div
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={
              uploadProgress !== null
                ? Math.round(uploadProgress.fraction * 100)
                : undefined
            }
            aria-label="Upload progress"
            style={progressTrackStyle}
            data-testid={`${testIdPrefix}-progress-bar`}
          >
            <div
              style={{
                ...progressFillStyle,
                width:
                  uploadProgress !== null
                    ? `${Math.min(100, Math.max(0, uploadProgress.fraction * 100))}%`
                    : "10%",
              }}
            />
          </div>
          <span
            style={progressLabelStyle}
            data-testid={`${testIdPrefix}-progress-label`}
          >
            {formatUploadProgress(uploadProgress)}
          </span>
        </div>
      ) : null}

      {validationError !== null ? (
        <div
          role="alert"
          style={errorStyle}
          data-testid={`${testIdPrefix}-validation-error`}
        >
          {validationError.message}
        </div>
      ) : null}
      {serverError !== null ? (
        <div
          role="alert"
          style={errorStyle}
          data-testid={`${testIdPrefix}-server-error`}
        >
          {serverError}
        </div>
      ) : null}
      {previewQuery.isError && pendingPreview === null ? (
        <div
          role="alert"
          style={errorStyle}
          data-testid={`${testIdPrefix}-fetch-error`}
        >
          Could not load preview: {formatApiError(previewQuery.error)}
        </div>
      ) : null}
    </div>
  );
}

/**
 * Renders the upload progress as a human-readable label. Exported for
 * unit tests; consumed by the in-component progress widget. Returns
 * an indeterminate label when the total is unknown (e.g. the browser
 * could not compute the content length).
 */
export function formatUploadProgress(p: UploadProgress | null): string {
  if (p === null) return "Starting…";
  if (p.total <= 0) {
    return `Uploading… ${formatBytes(p.loaded)}`;
  }
  const pct = Math.min(100, Math.max(0, Math.round(p.fraction * 100)));
  return `Uploading… ${pct}% (${formatBytes(p.loaded)} / ${formatBytes(p.total)})`;
}

function formatApiError(err: unknown): string {
  if (err instanceof ApiError) {
    return `${err.message} (${err.code})`;
  }
  if (err instanceof Error) return err.message;
  return "Unknown error";
}

// ---------------------------------------------------------------------------
// Styles (inline to match the surrounding admin app's styling conventions)
// ---------------------------------------------------------------------------

const containerStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  alignItems: "flex-start",
  maxWidth: 360,
};
const previewWrapStyle: CSSProperties = {
  width: 180,
  height: 120,
  border: "1px solid #d4d4d8",
  borderRadius: 6,
  background: "#fafafa",
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  overflow: "hidden",
};
const previewImgStyle: CSSProperties = {
  maxWidth: "100%",
  maxHeight: "100%",
  objectFit: "contain",
  display: "block",
};
const emptyPreviewStyle: CSSProperties = {
  color: "#71717a",
  fontSize: 12,
};
const controlsStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  flexWrap: "wrap",
};
const primaryBtnStyle: CSSProperties = {
  padding: "6px 12px",
  background: "#1f2937",
  color: "#fff",
  border: "1px solid #1f2937",
  borderRadius: 4,
  cursor: "pointer",
  fontSize: 13,
};
const secondaryBtnStyle: CSSProperties = {
  padding: "6px 12px",
  background: "#fff",
  color: "#1f2937",
  border: "1px solid #d4d4d8",
  borderRadius: 4,
  cursor: "pointer",
  fontSize: 13,
};
const hiddenInputStyle: CSSProperties = {
  position: "absolute",
  width: 1,
  height: 1,
  padding: 0,
  margin: -1,
  overflow: "hidden",
  clip: "rect(0,0,0,0)",
  whiteSpace: "nowrap",
  border: 0,
};
const hintStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#52525b",
};
const progressWrapStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  width: "100%",
};
const progressTrackStyle: CSSProperties = {
  width: "100%",
  height: 8,
  background: "#e4e4e7",
  borderRadius: 4,
  overflow: "hidden",
};
const progressFillStyle: CSSProperties = {
  height: "100%",
  background: "#1f2937",
  transition: "width 120ms linear",
};
const progressLabelStyle: CSSProperties = {
  fontSize: 12,
  color: "#52525b",
};
const errorStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#991b1b",
  background: "#fef2f2",
  border: "1px solid #fecaca",
  borderRadius: 4,
  padding: "6px 8px",
};
