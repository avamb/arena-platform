/**
 * A labelled text input or select with an optional hint and an inline
 * reason (4-7). The label is a real <label>, the hint and the reason are
 * tied to the control with aria-describedby, and a refused value is
 * aria-invalid, so a state is never colour only.
 */
import { useId, type ChangeEvent, type ReactNode } from "react";

interface BaseProps {
  label: string;
  hint?: string;
  /** The reason the value is refused, already translated. */
  error?: string;
  testId?: string;
}

export interface TextFieldProps extends BaseProps {
  value: string;
  onChange: (value: string) => void;
  type?: "text" | "email" | "tel";
  autoComplete?: string;
  maxLength?: number;
  /** The person left the field (6-1a: a field shows its error only once it was reached). */
  onBlur?: () => void;
}

export function TextField({ label, hint, error, testId, value, onChange, type = "text", autoComplete, maxLength, onBlur }: TextFieldProps) {
  const id = useId();
  const described = [hint !== undefined ? `${id}-hint` : null, error !== undefined ? `${id}-err` : null].filter(Boolean).join(" ");
  return (
    <>
      <label htmlFor={id}>{label}</label>
      <input
        id={id}
        type={type}
        value={value}
        autoComplete={autoComplete ?? "off"}
        maxLength={maxLength}
        aria-invalid={error !== undefined ? true : undefined}
        aria-describedby={described === "" ? undefined : described}
        data-testid={testId}
        onChange={(e: ChangeEvent<HTMLInputElement>) => onChange(e.target.value)}
        onBlur={onBlur}
      />
      {hint !== undefined && (
        <p className="field-hint" id={`${id}-hint`}>
          {hint}
        </p>
      )}
      {error !== undefined && (
        <p className="field-error" id={`${id}-err`} data-testid={testId !== undefined ? `${testId}-error` : undefined}>
          {error}
        </p>
      )}
    </>
  );
}

export interface SelectFieldProps extends BaseProps {
  value: string;
  onChange: (value: string) => void;
  children: ReactNode;
  onBlur?: () => void;
}

export function SelectField({ label, hint, error, testId, value, onChange, children, onBlur }: SelectFieldProps) {
  const id = useId();
  const described = [hint !== undefined ? `${id}-hint` : null, error !== undefined ? `${id}-err` : null].filter(Boolean).join(" ");
  return (
    <>
      <label htmlFor={id}>{label}</label>
      <select
        id={id}
        value={value}
        aria-invalid={error !== undefined ? true : undefined}
        aria-describedby={described === "" ? undefined : described}
        data-testid={testId}
        onChange={(e: ChangeEvent<HTMLSelectElement>) => onChange(e.target.value)}
        onBlur={onBlur}
      >
        {children}
      </select>
      {hint !== undefined && (
        <p className="field-hint" id={`${id}-hint`}>
          {hint}
        </p>
      )}
      {error !== undefined && (
        <p className="field-error" id={`${id}-err`} data-testid={testId !== undefined ? `${testId}-error` : undefined}>
          {error}
        </p>
      )}
    </>
  );
}

export interface TextAreaFieldProps extends BaseProps {
  value: string;
  onChange: (value: string) => void;
  maxLength?: number;
  rows?: number;
  onBlur?: () => void;
}

export function TextAreaField({ label, hint, error, testId, value, onChange, maxLength, rows = 4, onBlur }: TextAreaFieldProps) {
  const id = useId();
  const described = [hint !== undefined ? `${id}-hint` : null, error !== undefined ? `${id}-err` : null].filter(Boolean).join(" ");
  return (
    <>
      <label htmlFor={id}>{label}</label>
      <textarea
        id={id}
        value={value}
        rows={rows}
        maxLength={maxLength}
        aria-invalid={error !== undefined ? true : undefined}
        aria-describedby={described === "" ? undefined : described}
        data-testid={testId}
        onChange={(e: ChangeEvent<HTMLTextAreaElement>) => onChange(e.target.value)}
        onBlur={onBlur}
      />
      {hint !== undefined && (
        <p className="field-hint" id={`${id}-hint`}>
          {hint}
        </p>
      )}
      {error !== undefined && (
        <p className="field-error" id={`${id}-err`} data-testid={testId !== undefined ? `${testId}-error` : undefined}>
          {error}
        </p>
      )}
    </>
  );
}
