// @vitest-environment jsdom
/** The shared "Save changes" bar: disabled while nothing changed, enabled once something did, disabled while saving. */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SaveBar, type SaveBarProps } from "@/components/SaveBar";
import { I18nProvider, type Locale } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
afterEach(() => {
  for (const r of roots.splice(0)) act(() => r.unmount());
  document.body.innerHTML = "";
});

function mount(props: Partial<SaveBarProps>, locale: Locale = "en") {
  for (const r of roots.splice(0)) act(() => r.unmount());
  document.body.innerHTML = "";
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push(root);
  const onSave = props.onSave ?? vi.fn();
  act(() => {
    root.render(
      <I18nProvider locale={locale}>
        <SaveBar dirty={false} busy={false} onSave={onSave} {...props} />
      </I18nProvider>,
    );
  });
  return { onSave, button: document.querySelector<HTMLButtonElement>('[data-testid="save-changes"]')!, bar: document.querySelector<HTMLElement>('[data-testid="save-changes-bar"]')! };
}

describe("SaveBar", () => {
  it("is disabled while nothing changed", () => {
    const { button } = mount({});
    expect(button.textContent).toBe("Save changes");
    expect(button.disabled).toBe(true);
  });

  it("is enabled as soon as something changed, says so, and saves on a press", () => {
    const { button, bar, onSave } = mount({ dirty: true });
    expect(button.disabled).toBe(false);
    expect(bar.textContent).toContain("unsaved changes");
    act(() => button.click());
    expect(onSave).toHaveBeenCalledTimes(1);
  });

  it("is disabled again while saving and when a field is wrong", () => {
    expect(mount({ dirty: true, busy: true }).button.disabled).toBe(true);
    expect(mount({ dirty: true, blocked: true }).button.disabled).toBe(true);
  });

  it("goes back to disabled once the parent reports the save as done", () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    roots.push(root);
    const render = (dirty: boolean) =>
      act(() => {
        root.render(
          <I18nProvider locale="en">
            <SaveBar dirty={dirty} busy={false} onSave={() => {}} />
          </I18nProvider>,
        );
      });
    render(true);
    expect(host.querySelector<HTMLButtonElement>("button")!.disabled).toBe(false);
    render(false);
    expect(host.querySelector<HTMLButtonElement>("button")!.disabled).toBe(true);
  });

  it("speaks Russian and Spanish", () => {
    expect(mount({}, "ru").button.textContent).toBe("Сохранить изменения");
    expect(mount({}, "es").button.textContent).toBe("Guardar cambios");
  });
});
