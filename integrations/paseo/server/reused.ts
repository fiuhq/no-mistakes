/**
 * One read shared by every caller while it runs and until it is `ttlMs` old after it settles; a failed read is
 * dropped so the next caller tries again.
 */
export class Reused<T> {
  private last: { startedAt: number; settledAt: number | null; value: Promise<T> } | null = null;

  constructor(
    private readonly read: () => Promise<T>,
    private readonly ttlMs: number,
    private readonly now: () => number = Date.now,
  ) {}

  /** The current value; `since` asks for a read that started no earlier than that time. */
  get(since?: number): Promise<T> {
    const now = this.now();
    const last = this.last;
    if (last) {
      if (since !== undefined ? last.startedAt >= since : last.settledAt === null || now - last.settledAt < this.ttlMs) return last.value;
    }
    const value = this.read();
    const entry = { startedAt: now, settledAt: null as number | null, value };
    this.last = entry;
    value.then(
      () => {
        if (this.last === entry) entry.settledAt = this.now();
      },
      () => {
        if (this.last === entry) this.last = null;
      },
    );
    return value;
  }
}
