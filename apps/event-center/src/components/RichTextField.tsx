/**
 * RT-11 (spec 44): the field for a formatted text. It replaces TextAreaField
 * where a text carries bold, italic, links and lists: the description in the
 * wizard, the text in the buyer letters, the message about a moved or
 * canceled session. The value is "text Arena" (ART), the HTML subset the
 * server's filter accepts as it is; the editor library (`@arena/rich-editor`)
 * does the editing.
 *
 * The library is loaded when the field first appears, in its own chunk, so the
 * pages that have no formatted text do not carry it. Its stylesheet is a file
 * (`style-src 'self'`): the editor injects no CSS and sets no style attribute.
 *
 * The "Format" button (a model adds only the marks, the code proves that no
 * word changed) is on when `orgId` is given: it calls
 * POST /v1/organizations/{org_id}/texts/format. An answer with
 * `ai_applied: false` is a normal answer (the plain conversion of the text);
 * the editor says so in a neutral line under the field, never as an error.
 */
import "@arena/rich-editor/rich-editor.css";
import { useEffect, useId, useRef, useState } from "react";
import type { RichEditor } from "@arena/rich-editor";
import { formatText } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import type { ProfileName } from "@/lib/richText";

export interface RichTextFieldProps {
  /** Which text this is: the tags, the link schemes and the limits come from the profile. */
  profile: ProfileName;
  /** The text as ART ("" when empty). */
  value: string;
  /** Called after every edit of the person with the canonical ART. */
  onChange: (art: string) => void;
  label: string;
  hint?: string;
  /** The reason the value is refused, already translated. */
  error?: string;
  disabled?: boolean;
  /** The organization: turns the "Format" button on. Without it the field has no such button. */
  orgId?: string;
  testId?: string;
  onBlur?: () => void;
}

export function RichTextField({ profile, value, onChange, label, hint, error, disabled = false, orgId, testId, onBlur }: RichTextFieldProps) {
  const { t, locale } = useI18n();
  const ids = useId();
  const labelId = `${ids}-label`;
  const hintId = `${ids}-hint`;
  const errorId = `${ids}-err`;
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<RichEditor | null>(null);
  const [state, setState] = useState<"loading" | "ready" | "failed">("loading");
  // The callbacks and the value the editor was made with must not recreate it on every render.
  const live = useRef({ value, onChange, onBlur, disabled });
  live.current = { value, onChange, onBlur, disabled };
  // What the editor itself reported since the page last took a value from outside. The parent hands these back
  // one render later, maybe after more typing: such an echo must never be put into the editor again.
  const emitted = useRef<{ seen: Set<string>; last: string | null }>({ seen: new Set(), last: null });

  useEffect(() => {
    let cancelled = false;
    let made: RichEditor | null = null;
    setState("loading");
    import("@arena/rich-editor")
      .then(({ create }) => {
        const el = host.current;
        if (cancelled || el === null) return;
        made = create(el, {
          profile,
          locale,
          value: live.current.value,
          labelledBy: labelId,
          disabled: live.current.disabled,
          onChange: (html) => {
            emitted.current.seen.add(html);
            emitted.current.last = html;
            live.current.onChange(html);
          },
          format:
            orgId === undefined
              ? undefined
              : ({ text, profile: kind }) => formatText(orgId, { text, kind, locale }),
        });
        editor.current = made;
        setState("ready");
      })
      .catch(() => {
        if (!cancelled) setState("failed");
      });
    return () => {
      cancelled = true;
      made?.destroy();
      if (editor.current === made) editor.current = null;
    };
  }, [profile, locale, orgId, labelId]);

  // A value that arrives from outside (poster hints applied, a draft restored) goes into the editor. The editor's
  // own output comes back too, and may come back late (the person kept typing): it changes nothing.
  useEffect(() => {
    const e = editor.current;
    if (state !== "ready" || e === null) return;
    const mine = emitted.current;
    if (mine.seen.has(value)) {
      if (value === mine.last) mine.seen.clear(); // the parent has caught up
      return;
    }
    mine.seen.clear();
    mine.last = null;
    if (e.getHTML() !== value) e.setHTML(value);
  }, [value, state]);

  useEffect(() => {
    if (state === "ready") editor.current?.setDisabled(disabled);
  }, [disabled, state]);

  const described = [hint !== undefined ? hintId : null, error !== undefined ? errorId : null].filter(Boolean).join(" ");
  return (
    <div className="rich-text-field" data-testid={testId}>
      <label id={labelId} onClick={() => editor.current?.focus()}>
        {label}
      </label>
      <div
        role="group"
        aria-labelledby={labelId}
        aria-describedby={described === "" ? undefined : described}
        aria-invalid={error !== undefined ? true : undefined}
        onBlur={(e) => {
          // Only when the focus leaves the whole field, not when it moves from the text to the toolbar.
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) live.current.onBlur?.();
        }}
      >
        <div ref={host} data-testid={testId !== undefined ? `${testId}-editor` : undefined} />
        {state === "loading" && (
          <p className="field-hint" role="status">
            {t("richText.loading")}
          </p>
        )}
        {state === "failed" && (
          <p className="field-error" role="alert" data-testid={testId !== undefined ? `${testId}-load-error` : undefined}>
            {t("richText.loadFailed")}
          </p>
        )}
      </div>
      {hint !== undefined && (
        <p className="field-hint" id={hintId}>
          {hint}
        </p>
      )}
      {error !== undefined && (
        <p className="field-error" id={errorId} data-testid={testId !== undefined ? `${testId}-error` : undefined}>
          {error}
        </p>
      )}
    </div>
  );
}
