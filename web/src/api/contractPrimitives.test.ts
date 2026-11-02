import { describe, expect, it } from "vitest";
import {
  isCanonicalIdentity,
  isNonNegativeSafeInteger,
  isPositiveSafeInteger,
} from "./contractPrimitives";

describe("wire contract primitives", () => {
  it.each([
    ["id-1", true], ["", false], [" id", false], ["id ", false],
    ["id\r", false], ["id\n", false], ["id\0", false], [42, false],
  ])("classifies canonical identity %j", (value, expected) => {
    expect(isCanonicalIdentity(value)).toBe(expected);
  });

  it.each([
    [0, false, true], [1, true, true], [-1, false, false],
    [1.5, false, false], [Number.NaN, false, false],
    [Number.POSITIVE_INFINITY, false, false],
    [Number.MAX_SAFE_INTEGER + 1, false, false],
  ])("classifies safe integer %j", (value, positive, nonNegative) => {
    expect(isPositiveSafeInteger(value)).toBe(positive);
    expect(isNonNegativeSafeInteger(value)).toBe(nonNegative);
  });
});
