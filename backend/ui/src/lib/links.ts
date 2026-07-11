/**
 * Link presentation hints derived from a URL's shape. Since libcodex
 * v0.15.0 the crosswalk carries 856 $3 as the locator's rdfs:label, which
 * the doc surfaces as the value's annotation and the editor prefers
 *; this heuristic remains the fallback for label-less locators
 * and grains ingested before that, and still decides thumbnail rendering.
 */
export interface LinkInfo {
  /** Friendly label for the link, or "" when the URL shape is unrecognized. */
  label: string;
  /** True when the URL points at an image renderable as a thumbnail. */
  image: boolean;
}

const IMAGE_EXT = /\.(jpe?g|png|gif|webp|avif)$/i;

/** Classifies a link URL for display; never throws on malformed input. */
export function linkInfo(url: string): LinkInfo {
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return { label: "", image: false };
  }
  const host = u.hostname.toLowerCase();
  const path = decodeURIComponent(u.pathname);
  const image = IMAGE_EXT.test(path);
  if (host === "link.overdrive.com") return { label: "OverDrive title page", image };
  if (host === "samples.overdrive.com") return { label: "Sample (excerpt)", image };
  if (host === "img1.od-cdn.com" || host.endsWith(".od-cdn.com")) {
    // OverDrive cover CDN: ImageType-100/Img100 is the full cover,
    // ImageType-200/Img200 the thumbnail rendition.
    if (path.includes("ImageType-200") || /Img200\./i.test(path)) return { label: "Cover thumbnail", image: true };
    return { label: "Cover image", image: true };
  }
  return { label: image ? "Image" : "", image };
}
