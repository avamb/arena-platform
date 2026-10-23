/**
 * Calls of the sales page materials of an event: gallery, videos, other
 * sellers, the map point and the photo report. One source of truth: the same
 * API the Telegram bot and the public page read. Nothing is kept in the
 * browser (no localStorage, no demo data).
 */
import { UPLOAD_TIMEOUT_MS, call } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type { MediaObject } from "@/lib/api/types";

export interface PhotoItem {
  id: string;
  media_id: string;
  position: number;
  caption: string | null;
}
export interface VideoItem {
  id: string;
  provider: string;
  video_id: string | null;
  url: string;
  position: number;
}
export interface PartnerItem {
  id: string;
  url: string;
  seller_name: string;
  position: number;
}
export interface MapPoint {
  latitude: number;
  longitude: number;
  label: string | null;
}
export interface ItemsResponse<T> {
  items: T[];
  max_items: number;
}

export type PhotoKind = "gallery" | "report";

const scope = (orgId: string, eventId: string) => ({ org_id: orgId, event_id: eventId });

const PHOTO_ROUTES = {
  gallery: {
    list: API.galleryList,
    add: API.galleryAdd,
    order: API.galleryOrder,
    patch: API.galleryPatch,
    del: API.galleryDelete,
    idParam: "item_id",
    orderKey: "item_ids",
    owner: "event_gallery",
  },
  report: {
    list: API.reportList,
    add: API.reportAdd,
    order: API.reportOrder,
    patch: API.reportPatch,
    del: API.reportDelete,
    idParam: "photo_id",
    orderKey: "photo_ids",
    owner: "event_report",
  },
} as const;

export function listPhotos(kind: PhotoKind, orgId: string, eventId: string, signal?: AbortSignal): Promise<ItemsResponse<PhotoItem>> {
  return call({ route: PHOTO_ROUTES[kind].list, params: scope(orgId, eventId), signal });
}

/** Stores a picture through the media API for the gallery or the photo report (`media.write`). */
export function uploadEventPhoto(
  kind: PhotoKind,
  orgId: string,
  file: Blob,
  contentType: string,
  signal?: AbortSignal,
): Promise<{ media_object: MediaObject }> {
  const form = new FormData();
  form.append("owner_type", PHOTO_ROUTES[kind].owner);
  form.append("org_id", orgId);
  form.append("content_type", contentType);
  form.append("file", file, contentType === "image/png" ? "photo.png" : "photo.jpg");
  return call({ route: API.mediaUpload, form, signal, timeoutMs: UPLOAD_TIMEOUT_MS });
}

export function addPhoto(kind: PhotoKind, orgId: string, eventId: string, mediaId: string, caption: string | null): Promise<PhotoItem> {
  return call({ route: PHOTO_ROUTES[kind].add, params: scope(orgId, eventId), body: { media_id: mediaId, caption } });
}

/** The caption is always sent: a blank one clears it. */
export function patchPhotoCaption(kind: PhotoKind, orgId: string, eventId: string, id: string, caption: string | null): Promise<PhotoItem> {
  const r = PHOTO_ROUTES[kind];
  return call({ route: r.patch, params: { ...scope(orgId, eventId), [r.idParam]: id }, body: { caption } });
}

export function deletePhoto(kind: PhotoKind, orgId: string, eventId: string, id: string): Promise<unknown> {
  const r = PHOTO_ROUTES[kind];
  return call({ route: r.del, params: { ...scope(orgId, eventId), [r.idParam]: id } });
}

/** Sends the WHOLE order; the server refuses a list that is not the exact set. */
export function orderPhotos(kind: PhotoKind, orgId: string, eventId: string, ids: string[]): Promise<ItemsResponse<PhotoItem>> {
  const r = PHOTO_ROUTES[kind];
  return call({ route: r.order, params: scope(orgId, eventId), body: { [r.orderKey]: ids } });
}

export function listVideos(orgId: string, eventId: string, signal?: AbortSignal): Promise<ItemsResponse<VideoItem>> {
  return call({ route: API.videoList, params: scope(orgId, eventId), signal });
}
export function addVideo(orgId: string, eventId: string, url: string): Promise<VideoItem> {
  return call({ route: API.videoAdd, params: scope(orgId, eventId), body: { url } });
}
export function deleteVideo(orgId: string, eventId: string, id: string): Promise<unknown> {
  return call({ route: API.videoDelete, params: { ...scope(orgId, eventId), video_id: id } });
}
export function orderVideos(orgId: string, eventId: string, ids: string[]): Promise<ItemsResponse<VideoItem>> {
  return call({ route: API.videoOrder, params: scope(orgId, eventId), body: { video_ids: ids } });
}

export function listPartners(orgId: string, eventId: string, signal?: AbortSignal): Promise<ItemsResponse<PartnerItem>> {
  return call({ route: API.partnerList, params: scope(orgId, eventId), signal });
}
export function addPartner(orgId: string, eventId: string, url: string, sellerName: string): Promise<PartnerItem> {
  return call({ route: API.partnerAdd, params: scope(orgId, eventId), body: { url, seller_name: sellerName } });
}
export function patchPartner(orgId: string, eventId: string, id: string, url: string, sellerName: string): Promise<PartnerItem> {
  return call({ route: API.partnerPatch, params: { ...scope(orgId, eventId), link_id: id }, body: { url, seller_name: sellerName } });
}
export function deletePartner(orgId: string, eventId: string, id: string): Promise<unknown> {
  return call({ route: API.partnerDelete, params: { ...scope(orgId, eventId), link_id: id } });
}
export function orderPartners(orgId: string, eventId: string, ids: string[]): Promise<ItemsResponse<PartnerItem>> {
  return call({ route: API.partnerOrder, params: scope(orgId, eventId), body: { link_ids: ids } });
}

/** The point, or `null` when the event has none (the API answers 404 `event.map_point_not_found`). */
export async function getMapPoint(orgId: string, eventId: string, signal?: AbortSignal): Promise<MapPoint | null> {
  try {
    return await call<MapPoint>({ route: API.mapPointGet, params: scope(orgId, eventId), signal });
  } catch (err) {
    if (typeof err === "object" && err !== null && (err as { code?: string }).code === "event.map_point_not_found") return null;
    throw err;
  }
}
export function putMapPoint(orgId: string, eventId: string, p: { latitude: number; longitude: number; label: string | null }): Promise<MapPoint> {
  return call({ route: API.mapPointPut, params: scope(orgId, eventId), body: p });
}
export function deleteMapPoint(orgId: string, eventId: string): Promise<unknown> {
  return call({ route: API.mapPointDelete, params: scope(orgId, eventId) });
}

export interface VenueAddressPatch {
  /** Keep the legacy address in sync so clearing structured lines cannot revive it. */
  address: string;
  address_line1: string | null;
  address_line2: string | null;
  postal_code: string | null;
  country: string | null;
}
/** Saves the venue's own postal address (`venue.update`, the owning organization only). */
export function patchVenueAddress(orgId: string, venueId: string, body: VenueAddressPatch): Promise<unknown> {
  return call({ route: API.venueUpdate, params: { org_id: orgId, id: venueId }, body });
}
