# Frontend lab.vicoworks.com

Build: `npm run build`. Deploy seluruh `dist`, termasuk halaman `about/index.html`, metadata workspace, `workspace.html`, `sitemap.xml`, `robots.txt`, `llms.txt`, dan `llms-full.txt`.

Verifikasi output dengan `npm run test:seo`. Logo header memprioritaskan `assets/logo.png` jika file tersebut ada. Pada aset yang tersedia saat perubahan ini, nama filenya `assets/android-chrome-192x192.png` (192 × 192), dipakai sebagai fallback dan ditampilkan 36 × 36 dengan `object-fit: contain`. Favicon di `public/favicon.ico` disalin dari `assets/favicon.ico`.

Identitas publik dan domain canonical ditetapkan di `src/site.ts`. Teks AI ada di `public/llms*.txt`; jika domain atau fungsi berubah, perbarui teks tersebut dan `public/robots.txt`. Sitemap dibuat saat build, tanpa tanggal modifikasi palsu.

## Environment

Di `frontend/.env` untuk development atau environment build frontend di Vercel:

```dotenv
# Hanya diperlukan jika gateway WebSocket memakai hostname berbeda dari frontend.
VITE_LAB_WS_URL=wss://YOUR-SYNC-DOMAIN/api/agent-lab/ws
```

Jika HTTPS dan WebSocket dilayani dari domain yang sama, variabel ini boleh dihilangkan; aplikasi memakai `wss://lab.vicoworks.com/api/agent-lab/ws`. Development memakai proxy Vite ke `127.0.0.1:8000`. Tidak perlu variabel SEO tambahan. Variabel `VITE_*` masuk ke JavaScript publik dan memerlukan build ulang saat diubah.

Di `.env` backend SYNC (root project, working directory binary Go):

```dotenv
NEON_DB_URL=postgresql://USER:PASSWORD@YOUR-NEON-POOLER/neondb?sslmode=require
ARL_ADDR=127.0.0.1:8000
ARL_AUTH_TOKEN=GANTI_DENGAN_TOKEN_ACAK_PANJANG
ARL_ALLOWED_ORIGINS=https://lab.vicoworks.com
ARL_SQS_QUEUE_URL=https://sqs.ap-southeast-1.amazonaws.com/ACCOUNT/QUEUE
AWS_REGION=ap-southeast-1
ARL_DB_MAX_CONNS=4
ARL_MAX_HTTP=48
ARL_MAX_WS=100
```

Isi Queue URL hasil deployment jika menggunakan worker Lambda/SQS. Untuk worker lokal tanpa SQS, kosongkan `ARL_SQS_QUEUE_URL` dan jalankan proses worker. Tambahkan origin localhost atau preview Vercel yang digunakan secara eksplisit jika dibutuhkan.

Environment worker ASYNC/Lambda atau worker lokal:

```dotenv
NEON_DB_URL=postgresql://USER:PASSWORD@YOUR-NEON-POOLER/neondb?sslmode=require
ARL_WORKER_CONCURRENCY=4
ARL_DB_MAX_CONNS=2
OPENROUTER_API_KEY=
OPENAI_API_KEY=
GROQ_API_KEY=
# Hanya jika menggunakan endpoint lokal/OpenAI-compatible yang terjangkau worker:
ARL_LOCAL_BASE_URL=http://localhost:11434/v1
```

Isi key hanya untuk provider yang dipakai. Backend SYNC tidak membutuhkan provider key jika model calls dijalankan oleh worker terpisah. Jangan masukkan database URL, owner token, provider key, atau kredensial AWS ke frontend/VITE_*. Konfigurasi lengkap backend tersedia di `../.env.example` dan `../DEPLOYMENT.md`.

## Routing hosting

Untuk Vercel, pilih Root Directory `frontend`, build `npm run build`, Output Directory `dist`. Salin `vercel.json.example` menjadi `vercel.json` dan ganti `YOUR-SYNC-DOMAIN` dengan hostname gateway HTTPS yang nyata. Vercel memeriksa filesystem sebelum menerapkan rewrites. Contoh baru melayani file crawler dan halaman statis yang dihasilkan build, serta memakai `/workspace.html` dengan noindex untuk fallback rute dinamis. Sesuaikan fallback lama ke konfigurasi ini agar rute dinamis tidak menerima metadata homepage.

Untuk nginx/static hosting lain: layani file yang ada terlebih dahulu; `/about` mengarah ke `/about/index.html`; rute workspace tetap mengarah ke file `index.html` masing-masing; rute dinamis/unknown fallback ke `/workspace.html`. Proxy `/api/` ke backend dengan dukungan WebSocket/SSE seperti konfigurasi deployment backend. Pastikan MIME `.txt` adalah `text/plain`, `.xml` adalah `application/xml`, dan `.ico` adalah `image/x-icon`.

## Setelah deploy

1. Periksa `/`, `/about`, `/favicon.ico`, `/robots.txt`, `/sitemap.xml`, `/llms.txt`, dan `/llms-full.txt` melalui domain production. File crawler harus berisi teks/XML, bukan fallback HTML.
2. Periksa View Source `/about`: deskripsi fitur dan link sudah tersedia tanpa JavaScript. Workspace memakai `noindex`; sitemap hanya memuat `/` dan `/about`.
3. Verifikasi domain di Google Search Console dan Bing Webmaster Tools, lalu submit `https://lab.vicoworks.com/sitemap.xml`.
4. Pastikan preview deployment tidak dijadikan canonical production atau diindeks sebagai duplikat. Gunakan proteksi preview/noindex dari hosting.

`robots.txt` mengatur crawling dan bukan kontrol akses; autentikasi backend tetap menentukan akses data. `llms.txt`/`llms-full.txt` membantu mesin yang mendukungnya memahami fungsi situs, tanpa menjamin ranking atau pengutipan AI.

Referensi: [Google JavaScript SEO](https://developers.google.com/search/docs/crawling-indexing/javascript/javascript-seo-basics), [robots.txt](https://developers.google.com/search/docs/crawling-indexing/robots/intro), [llms.txt](https://llmstxt.org/), [Vercel configuration](https://vercel.com/docs/project-configuration/vercel-json).
