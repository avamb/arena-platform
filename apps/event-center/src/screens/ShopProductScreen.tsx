/**
 * A shop product (spec 47, section 6): create and edit in one form.
 *
 * ONE "Save" for the whole card (A-02): a bar that sticks to the top on a wide screen and sits at the bottom of a
 * phone. It sends what changed, in this order: the details (PATCH), the variants (PUT) and the descriptions of the
 * pictures, and says one thing at the end. A refused part stops the rest and is named where it belongs, with what
 * was already saved. While anything is unsaved, leaving the page asks first (`UnsavedChangesGuard`).
 *
 * - Details: name, description (the rich-text editor, "description" profile:
 *   the same HTML subset the server's sanitizer keeps), kind, status, the
 *   digital file, the names of up to three options. The address (slug) is
 *   read only: the server makes it from the name.
 * - Variants: one row per variant. A row whose stock the person did NOT touch
 *   sends no stock at all (SA-04); one they edited sends the new number and
 *   the one they saw (`expected_stock`). A 409 `shop.stock_changed` brings up
 *   to date ONLY the stock of the rows it names and says so: typed prices,
 *   SKUs and new rows stay. A 409 `shop.variants_changed` (a colleague added
 *   or removed a variant) merges the fresh set into the rows the same way.
 * - Details: the PATCH carries only the fields that changed (SA-13).
 * - Pictures: only for a saved product (they attach to its id).
 *
 * Creating sends the details AND the variants in one request. Editing saves
 * them with the API's separate routes, one after another.
 */
import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { SelectField, TextField } from "@/components/FormField";
import { RichTextField } from "@/components/RichTextField";
import { ShopImages } from "@/components/ShopImages";
import { NoShopAccess, ShopNav, ShopReadOnlyNote } from "@/components/ShopNav";
import { ShopVariantsEditor, type VariantStat } from "@/components/ShopVariantsEditor";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import { Badge, ErrorBox, Loading } from "@/components/ui";
import {
  attachShopImage,
  createShopProduct,
  deleteShopProduct,
  fetchShopProduct,
  fetchShopSettings,
  patchShopProduct,
  replaceShopVariants,
  uploadShopDigitalFile,
  type ShopProductFields,
} from "@/lib/api/shopApi";
import { currencyLabel } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { overMarkupLimit, overVisibleLimit } from "@/lib/richText";
import {
  PRODUCT_STATUSES,
  SHOP_KINDS,
  SHOP_LIMITS,
  checkVariants,
  emptyRow,
  isVariantsChanged,
  mergeVariantSet,
  productPatch,
  rowsAfterStockMoved,
  variantIdsOf,
  kindTracksStock,
  shelfEmpty,
  invalidFields,
  isSingleVersion,
  isStockChanged,
  stockChangedDetails,
  optionCountOf,
  optionNamesOf,
  productKind,
  rowsFromProduct,
  variantsChanged,
  variantsRequest,
  type ShopKind,
  type ShopProductView,
  type VariantRow,
  type VariantsCheck,
} from "@/lib/shop";
import { useOrg } from "@/screens/OrgLayout";

type SavedPart = "details" | "variants" | "images";

/** Three names, padded with empty strings. */
function padNames(names: readonly string[]): [string, string, string] {
  return [names[0] ?? "", names[1] ?? "", names[2] ?? ""];
}

/** The option names in use: the leading non-empty ones (option 2 needs option 1). */
function leadingNames(names: readonly string[]): string[] {
  const out: string[] = [];
  for (const n of names) {
    if (n.trim() === "") break;
    out.push(n.trim());
  }
  return out;
}

export function ProductForm({ orgId, currency, product, canManage, canViewMedia }: { orgId: string; currency: string; product?: ShopProductView; canManage: boolean; canViewMedia: boolean }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const editing = product !== undefined;
  const root = useRef<HTMLDivElement>(null);
  // Bumped on every failed attempt to save: the effect below then takes the person to the first wrong field.
  const [focusTick, setFocusTick] = useState(0);

  const [title, setTitle] = useState(product?.title ?? "");
  const [status, setStatus] = useState<string>(product?.status ?? "draft");
  const [kind, setKind] = useState<ShopKind>(product !== undefined ? productKind(product) : "physical");
  const [names, setNames] = useState<[string, string, string]>(padNames(product !== undefined ? optionNamesOf(product) : []));
  const [digitalId, setDigitalId] = useState<string | null>(product?.digital_media_id ?? null);
  const [body, setBody] = useState(product?.body_html ?? "");
  const bodyTouched = useRef(false);
  const [rows, setRows] = useState<VariantRow[]>(() => (product !== undefined ? rowsFromProduct(product, currency) : [emptyRow()]));
  const [check, setCheck] = useState<VariantsCheck | null>(null);
  const [detailsTried, setDetailsTried] = useState(false);
  const [busy, setBusy] = useState<"save" | "create" | "file" | null>(null);
  const [failure, setFailure] = useState<{ scope: "details" | "variants" | "images" | "file"; error: unknown } | null>(null);
  // The descriptions of the pictures the person typed and has not saved, by picture.
  const [alts, setAlts] = useState<Record<string, string>>({});
  // What a failed save had already stored, so the person knows what is left.
  const [savedParts, setSavedParts] = useState<SavedPart[]>([]);
  // Set right before the form itself navigates away (after a create or a delete): that move must not ask.
  const allowLeave = useRef(false);
  // The "unsaved changes" question is open: the Save bar steps aside so nothing covers its buttons (M-09).
  const [guardOpen, setGuardOpen] = useState(false);
  const [notice, setNotice] = useState<MessageKey | null>(null);
  const [fileProblem, setFileProblem] = useState<MessageKey | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [stockMoved, setStockMoved] = useState<{ title: string; expected: number | null; stock: number; typed: string }[]>([]);
  // The variant ids the rows were built from: the set the server is asked to still find (`expected_variant_ids`).
  const loadedIds = useRef<string[]>(product !== undefined ? variantIdsOf(product) : []);
  // What a merge after `shop.variants_changed` did, for the line under the variants.
  const [setMerged, setSetMerged] = useState<{ removed: number; added: number } | null>(null);

  const digital = kind === "digital";
  const used = leadingNames(names);
  const optionCount = optionCountOf(used, rows);
  const disabled = !canManage || busy !== null;

  // What differs from the saved product (editing) or from an empty form (creating).
  const patch = product !== undefined ? productPatch({ title, status, kind, optionNames: used, digitalId, body, bodyTouched: bodyTouched.current }, product) : {};
  const detailsDirty = product !== undefined && Object.keys(patch).length > 0;
  const variantsDirty = product !== undefined && variantsChanged(rows, product.variants, currency, optionCount, { digital });
  const altsDirty = product !== undefined && product.images.some((im) => alts[im.media_id] !== undefined && alts[im.media_id].trim() !== im.alt);
  const createDirty =
    product === undefined &&
    (title.trim() !== "" ||
      bodyTouched.current ||
      digitalId !== null ||
      names.some((n) => n.trim() !== "") ||
      rows.some((r) => r.price.trim() !== "" || r.sku.trim() !== "" || r.title.trim() !== "" || r.options.some((o) => o.trim() !== "")));
  const dirty = editing ? detailsDirty || variantsDirty || altsDirty : createDirty;

  const titleBad = title.trim() === "" || [...title.trim()].length > SHOP_LIMITS.title;
  const bodyBad = bodyTouched.current && (overVisibleLimit(body, "description") || overMarkupLimit(body, "description"));
  const detailsOk = !titleBad && !bodyBad;

  useEffect(() => {
    if (focusTick === 0) return;
    const first = root.current?.querySelector<HTMLElement>('[aria-invalid="true"]');
    if (first === null || first === undefined) return;
    first.scrollIntoView({ block: "center" });
    first.focus({ preventScroll: true });
  }, [focusTick]);

  // The names of the wrong fields, in page order, for the line next to the button.
  const detailsWrong = invalidFields({ title: detailsTried && titleBad, body: detailsTried && bodyBad }, null);
  const variantsWrong = invalidFields({ title: false, body: false }, check);
  const fixLine = (wrong: readonly string[]) =>
    wrong.length === 0 ? null : (
      <p className="field-error" role="alert" data-testid="shop-product-fix">
        {t("shop.product.fixFields", { fields: wrong.map((f) => t(`shop.product.field.${f}` as MessageKey)).join(", ") })}
      </p>
    );

  const stats: Record<string, VariantStat> = {};
  for (const v of product?.variants ?? []) stats[v.id] = { held: v.held, available: v.available ?? null };

  /** The whole form, for a NEW product. An existing one is patched with `productPatch` instead. */
  const fields = (): ShopProductFields => {
    const f: ShopProductFields = {
      title: title.trim(),
      status,
      kind,
      option_names: used,
    };
    // Absent keeps the file, null removes it. Only a digital product has one.
    if (kind === "digital") f.digital_media_id = digitalId;
    else if (product?.digital_media_id) f.digital_media_id = null;
    if (bodyTouched.current) f.body_html = body;
    return f;
  };

  const setProduct = (p: ShopProductView) => {
    queryClient.setQueryData(["shopProduct", orgId, p.id], p);
    void queryClient.invalidateQueries({ queryKey: ["shopProducts", orgId] });
  };

  const run = async (scope: "create" | "file", fn: () => Promise<void>) => {
    setBusy(scope);
    setStockMoved([]);
    setSetMerged(null);
    setFailure(null);
    setNotice(null);
    setSavedParts([]);
    try {
      await fn();
    } catch (error) {
      setFailure({ scope: scope === "create" ? "details" : "file", error });
    } finally {
      setBusy(null);
    }
  };

  /**
   * The variants step of the save. A 409 `shop.stock_changed` brings up to date only the stock of the rows it names
   * and stops the save; a 409 `shop.variants_changed` merges the colleague's set into the rows and stops it too.
   * Nothing the person typed is thrown away.
   */
  const saveVariantsStep = async (p: ShopProductView) => {
    try {
      const saved = await replaceShopVariants(orgId, p.id, variantsRequest(rows, currency, optionCount, { digital }), loadedIds.current);
      setProduct(saved);
      setRows(rowsFromProduct(saved, currency));
      loadedIds.current = variantIdsOf(saved);
      setCheck(null);
    } catch (error) {
      if (isStockChanged(error)) {
        const moved = stockChangedDetails(error);
        // The shelf moved while the screen was open: show the server's numbers beside the typed ones, change nothing else.
        setStockMoved(
          moved.map((d) => {
            const row = rows.find((r) => r.id === d.id);
            const name = row === undefined ? "" : row.title || row.options.filter((o) => o !== "").join(" / ");
            return { title: name !== "" ? name : isSingleVersion(rows, used) ? t("shop.variants.single") : "", expected: d.expected, stock: d.stock, typed: row?.stock ?? "" };
          }),
        );
        setProduct(await fetchShopProduct(orgId, p.id));
        setRows((list) => rowsAfterStockMoved(list, moved));
        setCheck(null);
      } else if (isVariantsChanged(error)) {
        const fresh = await fetchShopProduct(orgId, p.id);
        setProduct(fresh);
        const merged = mergeVariantSet(rows, fresh, loadedIds.current, currency);
        setRows(merged.rows);
        setSetMerged({ removed: merged.removed, added: merged.added });
        loadedIds.current = variantIdsOf(fresh);
        setCheck(null);
      }
      throw error;
    }
  };

  /** ONE save for the card: details, then variants, then the descriptions of the pictures. */
  const saveAll = async (): Promise<boolean> => {
    if (product === undefined || busy !== null || !canManage) return false;
    setDetailsTried(true);
    const c = checkVariants(rows, currency, optionCount, { digital });
    setCheck(c);
    if (!detailsOk || !c.ok) {
      setFocusTick((n) => n + 1);
      return false;
    }
    if (!dirty) return true;
    setBusy("save");
    setStockMoved([]);
    setSetMerged(null);
    setFailure(null);
    setNotice(null);
    setSavedParts([]);
    const done: SavedPart[] = [];
    let step: "details" | "variants" | "images" = "details";
    try {
      if (detailsDirty) {
        step = "details";
        const patched = await patchShopProduct(orgId, product.id, patch);
        setProduct(patched);
        // The text as the server stored it (it may have tidied the markup): the editor shows that, and nothing is "changed" any more.
        bodyTouched.current = false;
        setBody(patched.body_html ?? "");
        done.push("details");
      }
      if (variantsDirty) {
        step = "variants";
        await saveVariantsStep(product);
        done.push("variants");
      }
      if (altsDirty) {
        step = "images";
        let latest: ShopProductView = product;
        for (const im of product.images) {
          const typed = alts[im.media_id];
          if (typed === undefined || typed.trim() === im.alt) continue;
          latest = await attachShopImage(orgId, product.id, im.media_id, im.position, typed.trim());
        }
        setProduct(latest);
        setAlts({});
        done.push("images");
      }
      setNotice("shop.card.saved");
      return true;
    } catch (error) {
      setFailure({ scope: step, error });
      setSavedParts(done);
      return false;
    } finally {
      setBusy(null);
    }
  };

  /** A new product of another kind: its new rows count stock only for a physical one (M-01). Saved rows are left alone. */
  const changeKind = (next: ShopKind) => {
    setKind(next);
    const tracks = kindTracksStock(next);
    setRows((list) => list.map((r) => (r.id === undefined ? { ...r, trackStock: tracks } : r)));
  };

  const create = () => {
    setDetailsTried(true);
    const c = checkVariants(rows, currency, optionCount, { digital });
    setCheck(c);
    if (!detailsOk || !c.ok) {
      setFocusTick((n) => n + 1);
      return;
    }
    void run("create", async () => {
      const made = await createShopProduct(orgId, { ...fields(), variants: variantsRequest(rows, currency, optionCount, { digital }) });
      void queryClient.invalidateQueries({ queryKey: ["shopProducts", orgId] });
      queryClient.setQueryData(["shopProduct", orgId, made.id], made);
      allowLeave.current = true;
      await navigate({ to: "/o/$orgId/shop/products/$productId", params: { orgId, productId: made.id }, search: { created: true } });
    });
  };

  const onDigitalFile = (file: File | undefined) => {
    if (file === undefined) return;
    setFileProblem(null);
    if (file.size === 0) return setFileProblem("shop.product.digital.empty");
    if (file.size > SHOP_LIMITS.digitalBytes) return setFileProblem("shop.product.digital.tooLarge");
    void run("file", async () => {
      const up = await uploadShopDigitalFile(orgId, file);
      setDigitalId(up.media_object.id);
      setNotice("shop.product.digital.attached");
    });
  };

  const detailsError = failure !== null && failure.scope === "details" ? failure.error : null;
  const variantsError = failure !== null && failure.scope === "variants" ? failure.error : null;
  const imagesError = failure !== null && failure.scope === "images" ? failure.error : null;
  const fileError = failure !== null && failure.scope === "file" ? failure.error : null;
  const wrong = [...detailsWrong, ...variantsWrong.filter((f) => !detailsWrong.includes(f))];
  const barText: { key: MessageKey; tone: "ok" | "bad" | "plain" } | null =
    busy === "save"
      ? { key: "shop.card.saving", tone: "plain" }
      : failure !== null && failure.scope !== "file"
        ? { key: "shop.card.failed", tone: "bad" }
        : notice !== null
          ? { key: notice, tone: notice === "shop.card.saved" ? "ok" : "plain" }
          : dirty
            ? { key: "shop.card.dirty", tone: "plain" }
            : null;

  return (
    <div ref={root} className="shop-card">
      <UnsavedChangesGuard dirty={dirty} allowLeave={allowLeave} onOpenChange={setGuardOpen} onSaveAndLeave={canManage && editing ? saveAll : undefined} />
      {canManage && editing && (
        <div className="shop-savebar" data-testid="shop-savebar" hidden={guardOpen} aria-hidden={guardOpen ? true : undefined}>
          <button type="button" className="btn btn-primary" disabled={busy !== null || !dirty} onClick={() => void saveAll()} data-testid="shop-product-save">
            {busy === "save" ? t("shop.card.saving") : t("shop.card.save")}
          </button>
          <p className={`shop-savebar-status${barText?.tone === "bad" ? " field-error" : ""}`} role="status" aria-live="polite" data-testid="shop-savebar-status">
            {barText !== null && t(barText.key)}
            {failure !== null && failure.scope !== "file" && savedParts.length > 0 && (
              <> {t("shop.card.partial", { parts: savedParts.map((p) => t(`shop.card.part.${p}` as MessageKey)).join(", ") })}</>
            )}
          </p>
        </div>
      )}
      {canManage && editing && wrong.length > 0 && fixLine(wrong)}
      <h2>{t("shop.product.f.title")}</h2>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (editing) void saveAll();
          else create();
        }}
        noValidate
        data-testid="shop-product-form"
      >
        <TextField
          label={t("shop.product.f.title")}
          error={detailsTried && titleBad ? t("shop.product.err.title", { max: SHOP_LIMITS.title }) : undefined}
          value={title}
          maxLength={SHOP_LIMITS.title}
          onChange={setTitle}
          testId="shop-product-title"
        />
        <RichTextField
          profile="description"
          label={t("shop.product.f.body")}
          hint={t("shop.product.f.bodyHint")}
          error={detailsTried && bodyBad ? t("shop.product.err.body") : undefined}
          value={body}
          disabled={disabled}
          onChange={(v) => {
            bodyTouched.current = true;
            setBody(v);
          }}
          testId="shop-product-body"
        />
        <SelectField label={t("shop.product.f.kind")} hint={t(`shop.kind.hint.${kind}` as MessageKey)} value={kind} onChange={(v) => changeKind(v as ShopKind)} testId="shop-product-kind">
          {SHOP_KINDS.map((k) => (
            <option key={k} value={k}>
              {t(`shop.kind.${k}` as MessageKey)}
            </option>
          ))}
        </SelectField>
        {kind === "digital" && (
          <fieldset disabled={disabled}>
            <legend>{t("shop.product.digital.title")}</legend>
            <p className="field-hint">{t("shop.product.digital.hint", { mb: SHOP_LIMITS.digitalBytes / (1024 * 1024) })}</p>
            <p data-testid="shop-product-digital-state">{digitalId !== null ? t("shop.product.digital.have") : t("shop.product.digital.none")}</p>
            <input
              id="shop-digital-file"
              className="visually-hidden"
              type="file"
              onChange={(e) => {
                const f = e.target.files?.[0];
                e.target.value = "";
                onDigitalFile(f);
              }}
              data-testid="shop-product-digital-file"
            />
            <label htmlFor="shop-digital-file" className={`btn${busy === "file" ? " btn-disabled" : ""}`}>
              {busy === "file" ? t("shop.product.digital.uploading") : digitalId !== null ? t("shop.product.digital.replace") : t("shop.product.digital.choose")}
            </label>
            {fileProblem !== null && (
              <p className="field-error" role="alert">
                {t(fileProblem, { mb: SHOP_LIMITS.digitalBytes / (1024 * 1024) })}
              </p>
            )}
            {fileError !== null && <ErrorNotice error={fileError} />}
          </fieldset>
        )}
        <SelectField label={t("shop.product.f.status")} hint={t("shop.product.f.statusHint")} value={status} onChange={setStatus} testId="shop-product-status">
          {PRODUCT_STATUSES.map((s) => (
            <option key={s} value={s}>
              {t(`shop.product.status.${s}` as MessageKey)}
            </option>
          ))}
        </SelectField>
        {product !== undefined && (
          <>
            <label htmlFor="shop-product-slug">{t("shop.product.f.slug")}</label>
            <input id="shop-product-slug" type="text" value={product.slug} readOnly aria-describedby="shop-product-slug-hint" data-testid="shop-product-slug" />
            <p className="field-hint" id="shop-product-slug-hint">
              {t("shop.product.f.slugHint")}
            </p>
          </>
        )}
        <fieldset disabled={disabled}>
          <legend>{t("shop.options.title")}</legend>
          <p className="field-hint">{t("shop.options.hint", { max: SHOP_LIMITS.maxOptions })}</p>
          {[0, 1, 2].map((i) => (
            <TextField
              key={i}
              label={t("shop.options.name", { n: i + 1 })}
              value={names[i]}
              maxLength={SHOP_LIMITS.optionName}
              onChange={(v) => setNames((prev) => padNames(prev.map((p, j) => (j === i ? v : p))))}
              testId={`shop-option-name-${i + 1}`}
            />
          ))}
        </fieldset>
        {detailsError !== null && <ErrorNotice error={detailsError} testId="shop-product-error" />}
      </form>

      <section aria-labelledby="shop-variants-title">
        <h2 id="shop-variants-title">{t("shop.variants.title")}</h2>
        <p className="field-hint">{t("shop.variants.hint")}</p>
        <ShopVariantsEditor rows={rows} onChange={setRows} currency={currency} optionNames={used} optionCount={optionCount} check={check} disabled={disabled} digital={digital} newRowTracks={kindTracksStock(kind)} stats={stats} />
        {variantsError !== null && (
          <ErrorNotice error={variantsError} testId={isStockChanged(variantsError) ? "shop-stock-changed" : isVariantsChanged(variantsError) ? "shop-variants-changed" : "shop-variants-error"} />
        )}
        {variantsError !== null && isVariantsChanged(variantsError) && setMerged !== null && (
          <p className="notice notice-warn" role="status" data-testid="shop-variants-merged">
            {t("shop.variants.merged", { removed: setMerged.removed, added: setMerged.added })}
          </p>
        )}
        {variantsError !== null && isStockChanged(variantsError) && stockMoved.length > 0 && (
          <ul className="facts" data-testid="shop-stock-moved">
            {stockMoved.map((m, i) => (
              <li key={i}>{t("shop.variants.stockMoved", { title: m.title === "" ? `#${i + 1}` : m.title, was: m.expected ?? "", now: m.stock, typed: m.typed })}</li>
            ))}
          </ul>
        )}
      </section>

      {canManage && !editing && (
        <>
          {detailsError !== null && <ErrorNotice error={detailsError} testId="shop-product-error" />}
          <p>
            <button type="button" className="btn btn-primary" disabled={busy !== null} onClick={create} data-testid="shop-product-create">
              {busy === "create" ? t("shop.product.saving") : t("shop.product.create")}
            </button>
          </p>
          {fixLine([...detailsWrong, ...variantsWrong.filter((f) => !detailsWrong.includes(f))])}
        </>
      )}

      {product !== undefined ? (
        <ShopImages
          orgId={orgId}
          product={product}
          canManage={canManage}
          canView={canViewMedia}
          onProduct={setProduct}
          alts={alts}
          onAlt={(mediaId, text) => setAlts((prev) => ({ ...prev, [mediaId]: text }))}
          disabled={busy !== null}
          error={imagesError}
        />
      ) : (
        <section aria-labelledby="shop-images-title">
          <h2 id="shop-images-title">{t("shop.images.title")}</h2>
          <p className="field-hint" data-testid="shop-images-need-save">
            {t("shop.images.needSave")}
          </p>
        </section>
      )}

      {canManage && product !== undefined && (
        <p>
          <button type="button" className="btn" onClick={() => setDeleting(true)} data-testid="shop-product-delete">
            {t("shop.product.delete")}
          </button>
        </p>
      )}
      {deleting && product !== undefined && (
        <ConfirmPressDialog
          title={t("shop.product.delete.title")}
          subject={product.title}
          explain={t("shop.product.delete.explain")}
          confirmLabel={t("shop.product.delete")}
          testId="shop-product-delete-dialog"
          onCancel={() => setDeleting(false)}
          onConfirm={async () => {
            await deleteShopProduct(orgId, product.id);
            await queryClient.invalidateQueries({ queryKey: ["shopProducts", orgId] });
            queryClient.removeQueries({ queryKey: ["shopProduct", orgId, product.id] });
            allowLeave.current = true;
            await navigate({ to: "/o/$orgId/shop/products", params: { orgId }, search: { deleted: product.title } });
          }}
        />
      )}
    </div>
  );
}

function ProductPage({ productId }: { productId: string | null }) {
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const search = useSearch({ strict: false }) as { created?: boolean };
  const canRead = access.can("shop.read");
  const canManage = access.can("shop.manage");
  const settings = useQuery({ queryKey: ["shopSettings", orgId], queryFn: () => fetchShopSettings(orgId), enabled: canRead });
  const product = useQuery({
    queryKey: ["shopProduct", orgId, productId ?? ""],
    queryFn: () => fetchShopProduct(orgId, productId as string),
    enabled: canRead && productId !== null,
  });
  if (!canRead) return <NoShopAccess />;
  const currency = settings.data?.currency;
  const ready = currency !== undefined && (productId === null || product.data !== undefined);

  return (
    <section className="shop-page" aria-labelledby="shop-product-title-h">
      <h1 id="shop-product-title-h">{productId === null ? t("shop.product.titleNew") : (product.data?.title ?? t("shop.title"))}</h1>
      <ShopNav orgId={orgId} />
      {productId !== null && product.data !== undefined && product.data.status === "active" && shelfEmpty(product.data) && (
        <p data-testid="shop-product-soldout">
          <Badge tone="bad">{t("shop.product.soldOut")}</Badge>
        </p>
      )}
      <p>
        <Link to="/o/$orgId/shop/products" params={{ orgId }} search={{}}>
          {t("shop.product.back")}
        </Link>
      </p>
      {!canManage && <ShopReadOnlyNote />}
      {search.created === true && productId !== null && (
        <p className="notice notice-ok" role="status" data-testid="shop-product-created">
          {t("shop.product.created")}
        </p>
      )}
      {(settings.isLoading || product.isLoading) && <Loading />}
      {settings.error !== null && <ErrorBox error={settings.error} onRetry={() => void settings.refetch()} />}
      {product.error !== null && productId !== null && <ErrorBox error={product.error} onRetry={() => void product.refetch()} />}
      {ready && (
        <>
          <p className="muted" data-testid="shop-product-currency">
            {t("shop.product.currencyNote", { currency: currencyLabel(currency, locale) })}
          </p>
          <ProductForm
            key={productId ?? "new"}
            orgId={orgId}
            currency={currency}
            product={productId === null ? undefined : product.data}
            canManage={canManage}
            canViewMedia={access.can("media.read")}
          />
        </>
      )}
    </section>
  );
}

export function ShopProductNewScreen() {
  return <ProductPage productId={null} />;
}

export function ShopProductEditScreen() {
  const { productId } = useParams({ from: "/o/$orgId/shop/products/$productId" });
  return <ProductPage productId={productId} />;
}
