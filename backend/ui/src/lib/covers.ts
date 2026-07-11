// Cover URL resolution, shared by every admin surface that renders cover art.
import { apiBase } from "./config";

/** The img src for a stored cover value: an absolute feed/CDN URL is already
 *  resolved and renders verbatim; a site-relative editorial blob path takes
 *  the apiBase prefix and, when given, a post-replace cache-buster -- the
 *  CoverPanel corruption fix's contract, in one place. */
export function coverSrc(cover: string, bump?: number): string {
  if (!cover) return "";
  if (/^https?:\/\//.test(cover)) return cover;
  const v = bump === undefined ? "" : `?v=${bump}`;
  return `${apiBase()}/${cover}${v}`;
}
