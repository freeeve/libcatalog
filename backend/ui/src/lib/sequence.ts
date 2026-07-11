/** Guards debounced/refreshing fetches against out-of-order responses
 *: each call takes a ticket, and a response is applied only if
 *  its ticket is still the latest -- a slower earlier request can never
 *  overwrite a newer one's results.
 *
 *  const seq = sequencer();
 *  async function search(q: string) {
 *    const t = seq.take();
 *    const page = await fetchWorks(q);
 *    if (t.stale) return;
 *    results = page.works;
 *  }
 */
export interface Ticket {
  /** True once a later take() has superseded this call. */
  readonly stale: boolean;
}

export function sequencer(): { take(): Ticket } {
  let gen = 0;
  return {
    take(): Ticket {
      const g = ++gen;
      return {
        get stale(): boolean {
          return g !== gen;
        },
      };
    },
  };
}
