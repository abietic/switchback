import { describe, expect, it } from "vitest";
import { parseOptionalCommandReceipts } from "./contracts";

describe("optional command receipts", () => {
  it("preserves absence, valid pairs and identical duplicate collapse", () => {
    expect(parseOptionalCommandReceipts(undefined)).toBeUndefined();
    expect(parseOptionalCommandReceipts([
      { client_command_id: "client-1", command_id: "server-1" },
      { client_command_id: "client-1", command_id: "server-1" },
    ])).toEqual([
      { client_command_id: "client-1", command_id: "server-1" },
    ]);
  });

  it("preserves conflicting-pair rejection", () => {
    expect(() => parseOptionalCommandReceipts([
      { client_command_id: "client-1", command_id: "server-1" },
      { client_command_id: "client-1", command_id: "server-2" },
    ])).toThrow("command receipts are conflicting");
  });

  it.each(["", " client-1", "client-1 ", "client\r1", "client\n1", "client\0x", 42])(
    "preserves invalid client identity error for %j",
    (value) => {
      expect(() => parseOptionalCommandReceipts([
        { client_command_id: value, command_id: "server-1" },
      ])).toThrow("client command id is invalid");
    },
  );

  it.each(["", " server-1", "server-1 ", "server\r1", "server\n1", "server\0x", 42])(
    "preserves invalid server identity error for %j",
    (value) => {
      expect(() => parseOptionalCommandReceipts([
        { client_command_id: "client-1", command_id: value },
      ])).toThrow("server command id is invalid");
    },
  );
});
