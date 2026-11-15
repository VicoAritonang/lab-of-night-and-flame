export const SITE_URL = "https://lab.vicoworks.com";
export const SITE_NAME = "Lab Vico Aritonang | Lab Vicoworks";
export const SITE_DESCRIPTION = "Lab Vico Aritonang (Lab Vicoworks): an AI workbench for model benchmarks, reproducible experiments, router evaluation, and live AI agent tracing.";

export const FEATURES = [
  { id: "experiments", title: "Eksperimen dan benchmark model", href: "/experiments", text: "Bandingkan model pada snapshot dataset yang sama dengan rencana eksperimen tetap, pengulangan, retry, batas concurrency, dan anggaran. Nilai hasil dengan scorer deterministik atau judge, confidence interval, paired comparison, dan acceptance gate." },
  { id: "router-lab", title: "Router lab", href: "/router-lab", text: "Bandingkan router AI dengan model baseline. Periksa pilihan model per request, kualitas jawaban, latency, biaya, dan distribusi routing menurut kelompok tugas berdasarkan hasil campaign." },
  { id: "agent-lab", title: "Agent lab", href: "/agent-lab", text: "Amati service AI agent melalui telemetry WebSocket: fase input, retrieval, agent, dan output; graph node dan edge; kunjungan dan retry; nested span; request dan response model; pemanggilan tool; token, biaya, dan evaluasi. Telusuri kembali event melalui replay dan ekspor trace." },
  { id: "playground", title: "Playground dan katalog model", href: "/playground", text: "Uji prompt dan respons streaming, lalu pilih model dari koneksi provider yang dikonfigurasi: OpenRouter, OpenAI, Groq, atau endpoint lokal yang kompatibel. Demo dengan target fake tersedia untuk mencoba alur tanpa panggilan model berbayar." },
  { id: "datasets", title: "Dataset, scoring, dan laporan", href: "/datasets", text: "Impor dataset teks atau gambar dan prediction, gunakan snapshot dengan digest, lalu periksa hasil per trial dan slice. Lakukan rescore sebagai revision tanpa memanggil ulang target dan ekspor laporan JSON, HTML, atau Markdown." },
];

const TITLES: Record<string, string> = {
  "/playground": "Playground", "/models": "Models", "/datasets": "Datasets",
  "/experiments": "Experiments", "/experiments/new": "New experiment",
  "/router-lab": "Router lab", "/agent-lab": "Agent lab", "/workbench": "Workbench", "/settings": "Settings",
};
export const WORKSPACE_PATHS = Object.keys(TITLES);

export function pageMetadata(path: string) {
  const normalized = path.length > 1 ? path.replace(/\/+$/, "") : path;
  const isAbout = normalized === "/about";
  const indexable = normalized === "/" || isAbout;
  const title = isAbout ? `Tentang Lab AI | ${SITE_NAME}` : normalized === "/" ? `${SITE_NAME} — AI Experiments & Agent Tracing` : `${TITLES[normalized] ?? "Workspace"} | Lab Vicoworks`;
  const description = isAbout
    ? "Lab Vico Aritonang atau Lab Vicoworks: laboratorium AI untuk benchmark model, eksperimen, evaluasi router, dataset, scoring, dan pemantauan AI agent."
    : SITE_DESCRIPTION;
  return { title, description, url: `${SITE_URL}${normalized}`, lang: isAbout ? "id" : "en", indexable };
}

export function structuredData() {
  return {
    "@context": "https://schema.org",
    "@graph": [
      { "@type": "Person", "@id": `${SITE_URL}/#creator`, name: "Vico Aritonang" },
      { "@type": "Organization", "@id": `${SITE_URL}/#organization`, name: "Vicoworks", url: "https://vicoworks.com" },
      { "@type": "WebSite", "@id": `${SITE_URL}/#website`, url: `${SITE_URL}/`, name: "Lab Vico Aritonang", alternateName: ["Lab Vicoworks", "Lab of Night and Flame", "Agent Reliability Lab"], description: SITE_DESCRIPTION, creator: { "@id": `${SITE_URL}/#creator` }, publisher: { "@id": `${SITE_URL}/#organization` }, inLanguage: ["en", "id"] },
      { "@type": "SoftwareApplication", "@id": `${SITE_URL}/#application`, name: "Lab of Night and Flame", alternateName: ["Lab Vico Aritonang", "Lab Vicoworks"], url: `${SITE_URL}/`, applicationCategory: "DeveloperApplication", operatingSystem: "Web browser", description: SITE_DESCRIPTION, creator: { "@id": `${SITE_URL}/#creator` }, featureList: FEATURES.map((f) => f.title) },
    ],
  };
}

const escapeHTML = (value: string) => value.replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[character]!));

/** Shared by Vite's HTML transform and the static page build. */
export function renderMetadata(path: string) {
  const meta = pageMetadata(path);
  const title = escapeHTML(meta.title), description = escapeHTML(meta.description), url = escapeHTML(meta.url);
  return `<title>${title}</title>
    <meta name="description" content="${description}" />
    <meta name="author" content="Vico Aritonang" />
    <meta name="robots" content="${meta.indexable ? "index, follow, max-image-preview:large" : "noindex, follow"}" />
    <link rel="canonical" href="${url}" />
    <meta property="og:type" content="website" />
    <meta property="og:site_name" content="Lab Vicoworks" />
    <meta property="og:title" content="${title}" />
    <meta property="og:description" content="${description}" />
    <meta property="og:url" content="${url}" />
    <meta property="og:locale" content="${meta.lang === "id" ? "id_ID" : "en_US"}" />
    <meta name="twitter:card" content="summary" />
    <meta name="twitter:title" content="${title}" />
    <meta name="twitter:description" content="${description}" />
    <link rel="alternate" type="text/plain" href="${SITE_URL}/llms.txt" title="AI-readable site guide" />
    <script id="site-structured-data" type="application/ld+json">${JSON.stringify(structuredData()).replace(/</g, "\\u003c")}</script>`;
}
