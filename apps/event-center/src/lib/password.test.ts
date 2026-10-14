import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import { setPasswordErrorKey } from "@/lib/errors";
import { parsePasswordLink } from "@/lib/search";
import { checkNewPassword } from "@/screens/SetPasswordScreen";

describe("parsePasswordLink", () => {
  it("reads the token and the e-mail of the letter's link", () => {
    expect(parsePasswordLink("?token=abc123&email=anna%40example.test")).toEqual({
      token: "abc123",
      email: "anna@example.test",
    });
  });
  it("keeps a token that looks like a number as text", () => {
    expect(parsePasswordLink("?token=123e45").token).toBe("123e45");
  });
  it("drops an empty token and a value that is not an e-mail", () => {
    expect(parsePasswordLink("?token=&email=nobody")).toEqual({});
  });
});

describe("checkNewPassword", () => {
  it("asks for both fields", () => {
    expect(checkNewPassword("", "")).toBe("password.error.empty");
    expect(checkNewPassword("long enough", "")).toBe("password.error.empty");
  });
  it("counts bytes like the server", () => {
    expect(checkNewPassword("1234567", "1234567")).toBe("password.error.tooShort");
    // Four Cyrillic letters are eight bytes: long enough for the server.
    expect(checkNewPassword("пара", "пара")).toBeNull();
    const long = "я".repeat(37); // 74 bytes
    expect(checkNewPassword(long, long)).toBe("password.error.tooLong");
  });
  it("refuses two different passwords", () => {
    expect(checkNewPassword("password-1", "password-2")).toBe("password.error.mismatch");
  });
  it("accepts a good pair", () => {
    expect(checkNewPassword("a long phrase", "a long phrase")).toBeNull();
  });
});

describe("setPasswordErrorKey", () => {
  const api = (status: number, code: string) => new ApiError(status, code, code);
  it("names what is wrong with the link", () => {
    expect(setPasswordErrorKey(api(410, "auth.token_already_used"))).toBe("password.error.linkUsed");
    expect(setPasswordErrorKey(api(410, "auth.token_expired"))).toBe("password.error.linkExpired");
    expect(setPasswordErrorKey(api(404, "auth.token_not_found"))).toBe("password.error.linkInvalid");
  });
  it("names the password rule the server refused", () => {
    expect(setPasswordErrorKey(api(400, "validation.password_too_short"))).toBe("password.error.tooShort");
    expect(setPasswordErrorKey(api(400, "validation.password_too_long"))).toBe("password.error.tooLong");
  });
  it("falls back to the network and generic texts", () => {
    expect(setPasswordErrorKey(api(0, "network"))).toBe("common.error.network");
    expect(setPasswordErrorKey(new Error("boom"))).toBe("common.error.generic");
  });
});
