/**
 * The pictures of a shop product (spec 47, section 6): upload through the
 * media API (`owner_type=shop_product`), attach, describe, move, remove.
 *
 * There is no reorder route: "attaching a picture again moves and relabels
 * it", so a move re-attaches every picture with its new place (`moveImagePlan`).
 * The picture itself is fetched as a blob with `media.read` (the page's
 * Content-Security-Policy allows only blob and data images), like the event
 * poster and the gallery.
 */
import { useRef, useState } from "react";
import { Empty } from "@/components/ui";
import { ActionNotice, MoveButtons, PhotoThumb, useAction } from "@/components/materials/shared";
import { attachShopImage, detachShopImage, uploadShopImage } from "@/lib/api/shopApi";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { POSTER_ACCEPT, POSTER_MAX_BYTES, checkPosterFile, type PosterRejection } from "@/lib/posters";
import { SHOP_LIMITS, moveImagePlan, type ShopProductView } from "@/lib/shop";

const REJECT_KEY: Record<PosterRejection, MessageKey> = {
  empty: "poster.reject.empty",
  tooLarge: "poster.reject.tooLarge",
  notImage: "poster.reject.notImage",
  corrupt: "poster.reject.corrupt",
  tooBig: "poster.reject.tooBig",
};

/** The width asked of the media service for a picture of the product card (twice the size it is drawn at). */
const SHOP_THUMB_WIDTH = 480;

export interface ShopImagesProps {
  orgId: string;
  product: ShopProductView;
  canManage: boolean;
  /** `media.read`: the pictures can be shown. */
  canView: boolean;
  /** The product as the server now has it. */
  onProduct: (product: ShopProductView) => void;
}

export function ShopImages({ orgId, product, canManage, canView, onProduct }: ShopImagesProps) {
  const { t } = useI18n();
  const action = useAction();
  const [reject, setReject] = useState<PosterRejection | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const images = product.images;

  const onFile = async (file: File | undefined) => {
    if (fileInput.current !== null) fileInput.current.value = "";
    if (file === undefined) return;
    setReject(null);
    const checked = await checkPosterFile(file);
    if (!checked.ok) {
      setReject(checked.reason);
      return;
    }
    await action.run(async () => {
      const uploaded = await uploadShopImage(orgId, file, checked.info.type);
      onProduct(await attachShopImage(orgId, product.id, uploaded.media_object.id));
    });
  };

  const move = (index: number, delta: -1 | 1) => {
    const plan = moveImagePlan(images, index, delta);
    if (plan === null) return;
    void action.run(async () => {
      let latest: ShopProductView = product;
      for (const step of plan) latest = await attachShopImage(orgId, product.id, step.media_id, step.position, step.alt);
      onProduct(latest);
    });
  };

  return (
    <section className="mat-block" aria-labelledby="shop-images-title" data-testid="shop-images">
      <h2 id="shop-images-title">{t("shop.images.title")}</h2>
      <p className="field-hint">{t("shop.images.hint", { mb: POSTER_MAX_BYTES / (1024 * 1024) })}</p>
      {images.length === 0 && <Empty>{t("shop.images.empty")}</Empty>}
      <ul className="cards mat-photos">
        {images.map((im, index) => (
          <ImageRow
            key={`${im.media_id}:${im.alt}`}
            index={index}
            count={images.length}
            image={im}
            canManage={canManage}
            canView={canView}
            busy={action.busy}
            onAlt={(alt) =>
              void action.run(async () => {
                onProduct(await attachShopImage(orgId, product.id, im.media_id, im.position, alt));
              })
            }
            onMove={move}
            onRemove={() =>
              void action.run(async () => {
                onProduct(await detachShopImage(orgId, product.id, im.media_id));
              })
            }
          />
        ))}
      </ul>
      {canManage && (
        <div className="mat-add">
          <input
            ref={fileInput}
            id="shop-image-file"
            className="visually-hidden"
            type="file"
            accept={POSTER_ACCEPT}
            disabled={action.busy}
            onChange={(e) => void onFile(e.target.files?.[0])}
            data-testid="shop-image-file"
          />
          <label htmlFor="shop-image-file" className={`btn btn-primary${action.busy ? " btn-disabled" : ""}`}>
            {action.busy ? t("shop.images.uploading") : t("shop.images.add")}
          </label>
          {reject !== null && (
            <p className="field-error" role="alert" data-testid="shop-image-reject">
              {t(REJECT_KEY[reject], { mb: POSTER_MAX_BYTES / (1024 * 1024) })}
            </p>
          )}
        </div>
      )}
      <ActionNotice action={action} testId="shop-images" />
    </section>
  );
}

function ImageRow({
  index,
  count,
  image,
  canManage,
  canView,
  busy,
  onAlt,
  onMove,
  onRemove,
}: {
  index: number;
  count: number;
  image: { media_id: string; alt: string };
  canManage: boolean;
  canView: boolean;
  busy: boolean;
  onAlt: (alt: string) => void;
  onMove: (index: number, delta: -1 | 1) => void;
  onRemove: () => void;
}) {
  const { t } = useI18n();
  const [alt, setAlt] = useState(image.alt);
  const changed = alt.trim() !== image.alt;
  const id = `shop-image-${index}`;
  return (
    <li className="card mat-photo" data-testid="shop-image-row">
      <PhotoThumb mediaId={image.media_id} canView={canView} width={SHOP_THUMB_WIDTH} alt={image.alt !== "" ? image.alt : t("shop.images.alt.picture", { n: index + 1 })} />
      <div className="mat-photo-body">
        {index === 0 && <p className="card-meta">{t("shop.images.main")}</p>}
        <label className="field-label" htmlFor={`${id}-alt`}>
          {t("shop.images.alt", { n: index + 1 })}
        </label>
        <input
          id={`${id}-alt`}
          className="input"
          type="text"
          value={alt}
          maxLength={SHOP_LIMITS.alt}
          disabled={!canManage || busy}
          onChange={(e) => setAlt(e.target.value)}
          aria-describedby={`${id}-hint`}
          data-testid={`${id}-alt`}
        />
        <p className="field-hint" id={`${id}-hint`}>
          {t("shop.images.altHint")}
        </p>
        {canManage && (
          <div className="actions">
            <button type="button" className="btn btn-primary" disabled={busy || !changed} onClick={() => onAlt(alt.trim())} data-testid={`${id}-alt-save`}>
              {t("shop.images.altSave")}
            </button>
            <MoveButtons index={index} count={count} disabled={busy} onMove={(d) => onMove(index, d)} testId={id} />
            <button type="button" className="btn" disabled={busy} onClick={onRemove} data-testid={`${id}-remove`}>
              {t("shop.images.remove")}
            </button>
          </div>
        )}
      </div>
    </li>
  );
}
