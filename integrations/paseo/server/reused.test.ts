import { describe, expect, test } from "bun:test";
import { Reused } from "./reused";

describe("Reused", () => {
  test("shares one read inside the window and reads again after it", async () => {
    let now = 0;
    let reads = 0;
    const reused = new Reused(async () => ++reads, 15_000, () => now);
    expect(await Promise.all([reused.get(), reused.get()])).toEqual([1, 1]);
    now = 14_999;
    expect(await reused.get()).toBe(1);
    now = 15_000;
    expect(await reused.get()).toBe(2);
    expect(reads).toBe(2);
  });

  test("reads again on request inside the window", async () => {
    let reads = 0;
    const reused = new Reused(async () => ++reads, 15_000, () => 0);
    await reused.get();
    expect(await reused.get(true)).toBe(2);
    expect(await reused.get()).toBe(2);
  });

  test("drops a failed read so the next caller tries again", async () => {
    let fail = true;
    const reused = new Reused(
      async () => {
        if (fail) throw new Error("daemon busy");
        return "ok";
      },
      15_000,
      () => 0,
    );
    await expect(reused.get()).rejects.toThrow("daemon busy");
    fail = false;
    expect(await reused.get()).toBe("ok");
  });
});
