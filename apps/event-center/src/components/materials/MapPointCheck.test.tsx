import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { I18nProvider, type Locale } from "@/lib/i18n";
import { MapPointCheck } from "@/components/materials/MapPointCheck";

function render(lat: string, lng: string, locale: Locale = "en"): string {
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <MapPointCheck lat={lat} lng={lng} />
    </I18nProvider>,
  );
}

describe("the map point helper", () => {
  it("links the typed point to Google Maps and says where to put it", () => {
    const html = render("32.0853", "34.7818");
    expect(html).toContain('href="https://www.google.com/maps/search/?api=1&amp;query=32.0853,34.7818"');
    expect(html).toContain("Open this point in Google Maps");
    expect(html).toContain("Put the point at the entrance or the parking, where guests should arrive.");
  });

  it("shows only the hint while the point is empty, incomplete or out of range", () => {
    for (const [lat, lng] of [["", ""], ["32.1", ""], ["abc", "34"], ["95", "34"]]) {
      const html = render(lat, lng);
      expect(html).not.toContain("google.com");
      expect(html).toContain("entrance or the parking");
    }
  });

  it("treats 0 and 0 as a point", () => {
    expect(render("0", "0")).toContain("query=0,0");
  });

  it("speaks Russian and Spanish", () => {
    expect(render("1", "2", "ru")).toContain("Открыть эту точку в Google Картах");
    expect(render("1", "2", "es")).toContain("Abrir este punto en Google Maps");
  });
});
