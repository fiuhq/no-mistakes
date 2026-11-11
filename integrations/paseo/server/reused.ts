/** One read shared by every caller until it is `ttlMs` old; a failed read is dropped so the next caller tries again. */
export class Reused<T> {
  private last: { at: number; value: Promise<T> } | null = null;

  constructor(
    private readonly read: () => Promise<T>,
    private readonly ttlMs: number,
    private readonly now: () => number = Date.now,
  ) {}

  /** The current value; `fresh` reads again even inside the window. */
  get(fresh = false): Promise<T> {
    const at = this.now();
    if (!fresh && this.last && at - this.last.at < this.ttlMs) return this.last.value;
    const value = this.read();
    const entry = { at, value };
    this.last = entry;
    value.catch(() => {
      if (this.last === entry) this.last = null;
    });
    return value;
  }
}
