import { pageMetadata, structuredData } from "./site";

/** Keep client navigation consistent with the metadata in the built HTML. */
export function updateMetadata(path: string) {
  const meta = pageMetadata(path);
  document.title = meta.title;
  document.documentElement.lang = meta.lang;
  const setMeta = (attribute: "name" | "property", key: string, content: string) => {
    let element = document.head.querySelector<HTMLMetaElement>(`meta[${attribute}="${key}"]`);
    if (!element) { element = document.createElement("meta"); element.setAttribute(attribute, key); document.head.append(element); }
    element.content = content;
  };
  setMeta("name", "description", meta.description);
  setMeta("name", "robots", meta.indexable ? "index, follow, max-image-preview:large" : "noindex, follow");
  setMeta("property", "og:title", meta.title);
  setMeta("property", "og:description", meta.description);
  setMeta("property", "og:url", meta.url);
  setMeta("property", "og:locale", meta.lang === "id" ? "id_ID" : "en_US");
  setMeta("name", "twitter:title", meta.title);
  setMeta("name", "twitter:description", meta.description);
  let canonical = document.head.querySelector<HTMLLinkElement>('link[rel="canonical"]');
  if (!canonical) { canonical = document.createElement("link"); canonical.rel = "canonical"; document.head.append(canonical); }
  canonical.href = meta.url;
  const schema = document.getElementById("site-structured-data");
  if (schema) schema.textContent = JSON.stringify(structuredData());
}
