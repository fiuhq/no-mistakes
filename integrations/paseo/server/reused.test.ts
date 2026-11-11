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

  test("reads again when the last read started before `since`, and reuses one that started at or after it", async () => {
    let now = 0;
    let reads = 0;
    const reused = new Reused(async () => ++reads, 15_000, () => now);
    await reused.get();
    now = 10;
    expect(await reused.get(5)).toBe(2);
    expect(await reused.get(10)).toBe(2);
    expect(await reused.get()).toBe(2);
    expect(reads).toBe(2);
  });

  test("shares a read still running past the window and counts the window from when it settled", async () => {
    let now = 0;
    let reads = 0;
    let finish: (() => void) | null = null;
    const reused = new Reused(
      () =>
        new Promise<number>((resolve) => {
          const n = ++reads;
          finish = () => resolve(n);
        }),
      15_000,
      () => now,
    );
    const first = reused.get();
    now = 20_000;
    const second = reused.get();
    expect(reads).toBe(1);
    finish!();
    expect(await Promise.all([first, second])).toEqual([1, 1]);
    now = 34_999;
    expect(await reused.get()).toBe(1);
    expect(reads).toBe(1);
    now = 35_000;
    const third = reused.get();
    finish!();
    expect(await third).toBe(2);
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
