/**
 * A shop product (spec 47, section 6): create and edit in one form.
 *
 * - Details: name, description (the rich-text editor, "description" profile:
 *   the same HTML subset the server's sanitizer keeps), kind, status, the
 *   digital file, the names of up to three options. The address (slug) is
 *   read only: the server makes it from the name.
 * - Variants: one row per variant; a loaded row sends back the stock the
 *   person saw (`expected_stock`). A 409 `shop.stock_changed` reloads the
 *   rows from the server and says so; nothing is overwritten.
 * - Pictures: only for a saved product (they attach to its id).
 *
 * Creating sends the details AND the variants in one request. Editing saves
 * the details (PATCH) and the variants (PUT) with their own buttons, because
 * the API has two routes.
 */
import { useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { SelectField, TextField } from "@/components/FormField";
import { RichTextField } from "@/components/RichTextField";
import { ShopImages } from "@/components/ShopImages";
import { NoShopAccess, ShopNav, ShopReadOnlyNote } from "@/components/ShopNav";
import { ShopVariantsEditor, type VariantStat } from "@/components/ShopVariantsEditor";
import { ErrorBox, Loading } from "@/components/ui";
import {
  createShopProduct,
  deleteShopProduct,
  fetchShopProduct,
  fetchShopSettings,
  patchShopProduct,
  replaceShopVariants,
  uploadShopDigitalFile,
  type ShopProductFields,
} from "@/lib/api/shopApi";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { overMarkupLimit, overVisibleLimit } from "@/lib/richText";
import {
  PRODUCT_STATUSES,
  SHOP_KINDS,
  SHOP_LIMITS,
  checkVariants,
  emptyRow,
  isStockChanged,
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

function ProductForm({ orgId, currency, product, canManage, canViewMedia }: { orgId: string; currency: string; product?: ShopProductView; canManage: boolean; canViewMedia: boolean }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const editing = product !== undefined;

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
  const [busy, setBusy] = useState<"details" | "variants" | "create" | "file" | null>(null);
  const [failure, setFailure] = useState<{ scope: "details" | "variants" | "file"; error: unknown } | null>(null);
  const [notice, setNotice] = useState<MessageKey | null>(null);
  const [fileProblem, setFileProblem] = useState<MessageKey | null>(null);
  const [deleting, setDeleting] = useState(false);

  const used = leadingNames(names);
  const optionCount = optionCountOf(used, rows);
  const disabled = !canManage || busy !== null;

  const titleBad = title.trim() === "" || [...title.trim()].length > SHOP_LIMITS.title;
  const bodyBad = bodyTouched.current && (overVisibleLimit(body, "description") || overMarkupLimit(body, "description"));
  const detailsOk = !titleBad && !bodyBad;

  const stats: Record<string, VariantStat> = {};
  for (const v of product?.variants ?? []) stats[v.id] = { held: v.held, available: v.available ?? null };

  const fields = (): ShopProductFields => {
    const f: ShopProductFields = {
      title: title.trim(),
      status,
      kind,
      option_names: used,
      digital_media_id: kind === "digital" ? digitalId : null,
    };
    if (bodyTouched.current) f.body_html = body;
    return f;
  };

  const setProduct = (p: ShopProductView) => {
    queryClient.setQueryData(["shopProduct", orgId, p.id], p);
    void queryClient.invalidateQueries({ queryKey: ["shopProducts", orgId] });
  };

  const run = async (scope: "details" | "variants" | "create" | "file", fn: () => Promise<void>) => {
    setBusy(scope);
    setFailure(null);
    setNotice(null);
    try {
      await fn();
    } catch (error) {
      if (scope !== "create") setFailure({ scope, error });
      else setFailure({ scope: "details", error });
    } finally {
      setBusy(null);
    }
  };

  const saveDetails = () => {
    setDetailsTried(true);
    if (!detailsOk || product === undefined) return;
    void run("details", async () => {
      setProduct(await patchShopProduct(orgId, product.id, fields()));
      setNotice("shop.product.saved");
    });
  };

  const saveVariants = () => {
    if (product === undefined) return;
    const c = checkVariants(rows, currency, optionCount);
    setCheck(c);
    if (!c.ok) return;
    void run("variants", async () => {
      try {
        const saved = await replaceShopVariants(orgId, product.id, variantsRequest(rows, currency, optionCount));
        setProduct(saved);
        setRows(rowsFromProduct(saved, currency));
        setCheck(null);
        setNotice("shop.variants.saved");
      } catch (error) {
        if (!isStockChanged(error)) throw error;
        // The shelf moved while the screen was open: show the server's numbers, say so, change nothing else.
        const fresh = await fetchShopProduct(orgId, product.id);
        setProduct(fresh);
        setRows(rowsFromProduct(fresh, currency));
        setCheck(null);
        throw error;
      }
    });
  };

  const create = () => {
    setDetailsTried(true);
    const c = checkVariants(rows, currency, optionCount);
    setCheck(c);
    if (!detailsOk || !c.ok) return;
    void run("create", async () => {
      const made = await createShopProduct(orgId, { ...fields(), variants: variantsRequest(rows, currency, optionCount) });
      void queryClient.invalidateQueries({ queryKey: ["shopProducts", orgId] });
      queryClient.setQueryData(["shopProduct", orgId, made.id], made);
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
  const fileError = failure !== null && failure.scope === "file" ? failure.error : null;

  return (
    <>
      <h2>{t("shop.product.f.title")}</h2>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (editing) saveDetails();
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
        <SelectField label={t("shop.product.f.kind")} hint={t(`shop.kind.hint.${kind}` as MessageKey)} value={kind} onChange={(v) => setKind(v as ShopKind)} testId="shop-product-kind">
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
        {canManage && editing && (
          <p>
            <button type="submit" className="btn btn-primary" disabled={busy !== null} data-testid="shop-product-save">
              {busy === "details" ? t("shop.product.saving") : t("shop.product.save")}
            </button>
          </p>
        )}
      </form>
      {notice !== null && notice !== "shop.variants.saved" && (
        <p className="notice notice-ok" role="status" data-testid="shop-product-notice">
          {t(notice)}
        </p>
      )}

      <section aria-labelledby="shop-variants-title">
        <h2 id="shop-variants-title">{t("shop.variants.title")}</h2>
        <p className="field-hint">{t("shop.variants.hint")}</p>
        <ShopVariantsEditor rows={rows} onChange={setRows} currency={currency} optionNames={used} optionCount={optionCount} check={check} disabled={disabled} stats={stats} />
        {variantsError !== null && (
          <ErrorNotice error={variantsError} testId={isStockChanged(variantsError) ? "shop-stock-changed" : "shop-variants-error"} />
        )}
        {notice === "shop.variants.saved" && (
          <p className="notice notice-ok" role="status" data-testid="shop-variants-notice">
            {t(notice)}
          </p>
        )}
        {canManage && editing && product !== undefined && (
          <p>
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy !== null || !variantsChanged(rows, product.variants, currency, optionCount)}
              onClick={saveVariants}
              data-testid="shop-variants-save"
            >
              {busy === "variants" ? t("shop.variants.saving") : t("shop.variants.save")}
            </button>
          </p>
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
        </>
      )}

      {product !== undefined ? (
        <ShopImages orgId={orgId} product={product} canManage={canManage} canView={canViewMedia} onProduct={setProduct} />
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
            await navigate({ to: "/o/$orgId/shop/products", params: { orgId }, search: { deleted: product.title } });
          }}
        />
      )}
    </>
  );
}

function ProductPage({ productId }: { productId: string | null }) {
  const { t } = useI18n();
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
            {t("shop.product.currencyNote", { currency })}
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
