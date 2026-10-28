import { describe, expect, it } from "vitest";
import { tokenFromHash } from "./token";

describe("tokenFromHash", () => {
  it("reads the token", () => {
    expect(tokenFromHash("#token=abc-_123")).toBe("abc-_123");
  });
  it("ignores other params", () => {
    expect(tokenFromHash("#x=1&token=t&y=2")).toBe("t");
  });
  it("returns null when absent or empty", () => {
    expect(tokenFromHash("")).toBeNull();
    expect(tokenFromHash("#")).toBeNull();
    expect(tokenFromHash("#token=")).toBeNull();
  });
});
