/**
 * Calls of the shop (spec 47, section 6). One function per route of the
 * shop; every URL comes from the route table. Reads need `shop.read`,
 * writes `shop.manage`. Cancel and refund move money, so the caller passes
 * the step-up headers (`withStepUp`).
 */
import { UPLOAD_TIMEOUT_MS, call, download } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type {
  MediaObject,
  SalesChannelItem,
  ShopOrder,
  ShopOrderCounts,
  ShopOrderList,
  ShopProduct,
  ShopProductList,
  ShopRefundResponse,
  ShopResendEmailResponse,
  ShopSettings,
  ShopSettingsUpdateRequest,
} from "@/lib/api/types";
import { SHOP_DIGITAL_OWNER, SHOP_IMAGE_OWNER, type ShopVariantSend } from "@/lib/shop";

const org = (orgId: string) => ({ org_id: orgId });
const prod = (orgId: string, productId: string) => ({ org_id: orgId, product_id: productId });
const ord = (orgId: string, orderId: string) => ({ org_id: orgId, order_id: orderId });

// ─── Settings ────────────────────────────────────────────────────────────────

export function fetchShopSettings(orgId: string): Promise<ShopSettings> {
  return call<ShopSettings>({ route: API.shopSettings, params: org(orgId) });
}

export function saveShopSettings(orgId: string, body: ShopSettingsUpdateRequest): Promise<ShopSettings> {
  return call<ShopSettings>({ route: API.shopSettingsPut, params: org(orgId), body });
}

/** The channels of the organization (`channel.read`, the owner). */
export function listSalesChannels(orgId: string): Promise<{ channels: SalesChannelItem[] }> {
  return call<{ channels: SalesChannelItem[] }>({ route: API.channels, params: org(orgId) });
}

// ─── Products ────────────────────────────────────────────────────────────────

export function listShopProducts(orgId: string, status?: string): Promise<ShopProductList> {
  return call<ShopProductList>({ route: API.shopProducts, params: org(orgId), query: { status } });
}

export function fetchShopProduct(orgId: string, productId: string): Promise<ShopProduct> {
  return call<ShopProduct>({ route: API.shopProduct, params: prod(orgId, productId) });
}

export interface ShopProductFields {
  title?: string;
  body_html?: string;
  status?: string;
  slug?: string;
  product_type?: string;
  vendor?: string;
  kind?: string;
  digital_media_id?: string | null;
  option_names?: string[];
}

export function createShopProduct(orgId: string, body: ShopProductFields & { variants?: ShopVariantSend[] }): Promise<ShopProduct> {
  return call<ShopProduct>({ route: API.shopProductCreate, params: org(orgId), body });
}

export function patchShopProduct(orgId: string, productId: string, body: ShopProductFields): Promise<ShopProduct> {
  return call<ShopProduct>({ route: API.shopProductPatch, params: prod(orgId, productId), body });
}

export function deleteShopProduct(orgId: string, productId: string): Promise<void> {
  return call<void>({ route: API.shopProductDelete, params: prod(orgId, productId) });
}

/** The complete variant list. A variant left out is deleted. */
export function replaceShopVariants(orgId: string, productId: string, variants: ShopVariantSend[]): Promise<ShopProduct> {
  return call<ShopProduct>({ route: API.shopVariantsReplace, params: prod(orgId, productId), body: { variants } });
}

/**
 * Stores a file through the media API. Multipart, one `file` part. The answer is NOT flat:
 * `{ media_object }`. A picture is named `.jpg`/`.png` after the type the web proved from its
 * bytes, a digital file keeps the extension the person's file has.
 */
function uploadShopMedia(orgId: string, ownerType: string, file: Blob, contentType: string, fileName: string, signal?: AbortSignal) {
  const form = new FormData();
  form.append("owner_type", ownerType);
  form.append("org_id", orgId);
  form.append("content_type", contentType);
  form.append("file", file, fileName);
  return call<{ media_object: MediaObject }>({ route: API.mediaUpload, form, signal, timeoutMs: UPLOAD_TIMEOUT_MS });
}

export function uploadShopImage(orgId: string, file: Blob, contentType: string, signal?: AbortSignal) {
  return uploadShopMedia(orgId, SHOP_IMAGE_OWNER, file, contentType, contentType === "image/png" ? "product.png" : "product.jpg", signal);
}

export function uploadShopDigitalFile(orgId: string, file: File, signal?: AbortSignal) {
  const type = file.type !== "" ? file.type : "application/octet-stream";
  return uploadShopMedia(orgId, SHOP_DIGITAL_OWNER, file, type, safeFileName(file.name), signal);
}

/** A file name with no path and no odd characters: the server shows it, never trusts it. */
export function safeFileName(name: string): string {
  const base = name.split(/[\\/]/).pop() ?? "";
  const clean = base.replace(/[^\p{L}\p{N}._ -]/gu, "_").trim();
  return clean === "" ? "file" : clean.slice(0, 120);
}

/** Attaches an uploaded picture (or moves and relabels one that is attached). */
export function attachShopImage(orgId: string, productId: string, mediaId: string, position?: number, alt?: string): Promise<ShopProduct> {
  const body: { media_id: string; position?: number; alt?: string } = { media_id: mediaId };
  if (position !== undefined) body.position = position;
  if (alt !== undefined) body.alt = alt;
  return call<ShopProduct>({ route: API.shopImageAdd, params: prod(orgId, productId), body });
}

export function detachShopImage(orgId: string, productId: string, mediaId: string): Promise<ShopProduct> {
  return call<ShopProduct>({ route: API.shopImageRemove, params: { ...prod(orgId, productId), media_id: mediaId } });
}

// ─── Orders ──────────────────────────────────────────────────────────────────

export function listShopOrders(orgId: string, query: Record<string, string | number | undefined>): Promise<ShopOrderList> {
  return call<ShopOrderList>({ route: API.shopOrders, params: org(orgId), query });
}

/** How many orders each quick list holds (`to_ship`, `unpaid`, ...), in one request. */
export function fetchShopOrderCounts(orgId: string): Promise<ShopOrderCounts> {
  return call<ShopOrderCounts>({ route: API.shopOrderCounts, params: org(orgId) });
}

/** Writes (or, with an empty text, erases) the internal note of an order. The answer is the order. */
export function saveShopOrderNote(orgId: string, orderId: string, staffNote: string): Promise<ShopOrder> {
  return call<ShopOrder>({ route: API.shopOrderNote, params: ord(orgId, orderId), body: { staff_note: staffNote } });
}

/** Queues the buyer's letter once more. One request per order per minute: a second answers 429. */
export function resendShopOrderEmail(orgId: string, orderId: string, kind: "received" | "shipped"): Promise<ShopResendEmailResponse> {
  return call<ShopResendEmailResponse>({ route: API.shopOrderResendEmail, params: ord(orgId, orderId), body: { kind } });
}

export function fetchShopOrder(orgId: string, orderId: string): Promise<ShopOrder> {
  return call<ShopOrder>({ route: API.shopOrder, params: ord(orgId, orderId) });
}

export function shipShopOrder(orgId: string, orderId: string, trackingNote: string): Promise<ShopOrder> {
  const body: { tracking_note?: string } = trackingNote.trim() === "" ? {} : { tracking_note: trackingNote.trim() };
  return call<ShopOrder>({ route: API.shopOrderShip, params: ord(orgId, orderId), body });
}

/** Cancels an order that waits for payment. `headers` carries the step-up token. */
export function cancelShopOrder(orgId: string, orderId: string, headers: Record<string, string>): Promise<ShopOrder> {
  return call<ShopOrder>({ route: API.shopOrderCancel, params: ord(orgId, orderId), body: {}, headers });
}

/** Returns money. `headers` carries the step-up token. */
export function refundShopOrder(
  orgId: string,
  orderId: string,
  body: { amount_minor?: number; reason?: string },
  headers: Record<string, string>,
): Promise<ShopRefundResponse> {
  return call<ShopRefundResponse>({ route: API.shopOrderRefund, params: ord(orgId, orderId), body, headers });
}

/** The delivery details, one per line, ready to paste into a courier form. */
export async function fetchCourierText(orgId: string, orderId: string): Promise<string> {
  const file = await download({
    route: API.shopCourierText,
    params: ord(orgId, orderId),
    accept: "text/plain",
    fallbackName: "courier.txt",
  });
  return file.blob.text();
}
