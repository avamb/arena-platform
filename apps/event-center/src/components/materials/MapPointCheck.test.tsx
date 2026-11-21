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
    expect(html).toContain("Put the point at the entrance or at the car park, where guests must go.");
  });

  it("shows only the hint while the point is empty, incomplete or out of range", () => {
    for (const [lat, lng] of [["", ""], ["32.1", ""], ["abc", "34"], ["95", "34"]]) {
      const html = render(lat, lng);
      expect(html).not.toContain("google.com");
      expect(html).toContain("entrance or at the car park");
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

describe("the advisory warning", () => {
  const far = {
    code: "venue.point_far_from_city",
    message: "",
    city: "Netanya",
    distance_km: 20.5,
    threshold_km: 16.8,
    nearer_city: "Ramat HaSharon",
    nearer_distance_km: 0.3,
  };
  const renderWith = (w: typeof far, locale: Locale = "en") =>
    renderToStaticMarkup(
      <I18nProvider locale={locale}>
        <MapPointCheck lat="32.146" lng="34.839" warnings={[w]} />
      </I18nProvider>,
    );

  it("names the city, the distance and the nearer city, and says the save still works", () => {
    const html = renderWith(far);
    expect(html).toContain("20.5 km from Netanya");
    expect(html).toContain("closer to Ramat HaSharon (0.3 km)");
    expect(html).toContain("You can save the point");
    expect(html).toContain("google.com/maps");
  });

  it("has a text for a point that is near but in the wrong city", () => {
    const html = renderWith({ ...far, code: "venue.point_nearer_other_city", distance_km: 13.5 });
    expect(html).toContain("only 0.3 km from Ramat HaSharon");
  });

  it("shows no warning for an invalid point and speaks Russian", () => {
    const html = renderToStaticMarkup(
      <I18nProvider locale="ru">
        <MapPointCheck lat="" lng="" warnings={[far]} />
      </I18nProvider>,
    );
    expect(html).not.toContain("Netanya");
    expect(renderWith(far, "ru")).toContain("от города Netanya");
  });
});
