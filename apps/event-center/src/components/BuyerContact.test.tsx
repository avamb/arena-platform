import { describe, expect, it } from "vitest";
import { buyerInitials, telHref, whatsAppHref } from "@/components/BuyerContact";

describe("buyer contact", () => {
  it("takes the initials of the first and the last name, else of the e-mail", () => {
    expect(buyerInitials("Мария Лопес Гарсия", null)).toBe("МГ");
    expect(buyerInitials("anna", "anna@example.test")).toBe("A");
    expect(buyerInitials(null, "zoe@example.test")).toBe("Z");
    expect(buyerInitials(null, null)).toBe("?");
  });

  it("dials the digits and the plus", () => {
    expect(telHref("+34 600 12-34-56")).toBe("tel:+34600123456");
  });

  it("writes on WhatsApp only to an international number", () => {
    expect(whatsAppHref("+34 600 123 456")).toBe("https://wa.me/34600123456");
    expect(whatsAppHref("0034 600 123 456")).toBe("https://wa.me/34600123456");
    expect(whatsAppHref("12345")).toBeNull();
  });
});
