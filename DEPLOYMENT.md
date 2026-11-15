# Deployment

Ada dua cara menjalankan lab:

| Mode | Untuk | Cara |
| --- | --- | --- |
| **All-in-one (default)** | Development lokal / tim, berdampingan dengan agent yang sedang dibangun | `docker compose up -d` — satu container (API + WebSocket + UI + worker) dan PostgreSQL. Lihat [README](README.md#quick-start) dan [examples/compose](examples/compose/docker-compose.yml). Schema diterapkan otomatis (`ARL_AUTO_MIGRATE=true` di image). |
| **Split SYNC / ASYNC (AWS)** | Lab bersama yang di-host: EC2 + Lambda/SQS + Neon + Vercel | Dokumen di bawah. Aktif ketika `ARL_SQS_QUEUE_URL` diisi; worker in-process otomatis mati. |

Variabel penting all-in-one: `ARL_DATABASE_URL` (alias `NEON_DB_URL`), `ARL_AUTO_MIGRATE`, `ARL_EMBEDDED_WORKER` (default `true` tanpa SQS), `ARL_LISTEN_ON_START`, `ARL_SERVE_UI` (default `true`), `ARL_AUTH_TOKEN`. Daftar lengkap ada di [.env.example](.env.example).

---

# Split deployment Go SYNC / ASYNC

Semua perintah di bawah dijalankan dari root repository kecuali bagian frontend. Contoh shell deployment menggunakan Bash. Di PowerShell, environment proses dapat diatur dengan `$env:NAME='value'`. Tidak ada langkah yang memerlukan backend Python.

## 1. Init Neon PostgreSQL

Gunakan URL Neon dengan TLS, sebaiknya pooled endpoint (`-pooler`), dalam `NEON_DB_URL` pada `.env`. Runtime tidak menjalankan migration otomatis. Pilih salah satu:

```bash
# NEON_DB_URL sudah diekspor dalam terminal:
psql "$NEON_DB_URL" -v ON_ERROR_STOP=1 -f sql/init.sql

# Atau admin Go membaca .env tanpa perlu mengekspor:
go run ./cmd/admin init --apply
go run ./cmd/admin seed
```

`go run ./cmd/admin init` tanpa --apply hanya mencetak SQL. [sql/init.sql](sql/init.sql) memakai transaksi dan CREATE IF NOT EXISTS; tidak menghapus tabel pengguna. Schema `arl` menyimpan project, JSONB documents, jobs/outbox, stream events, sessions, traces, trace events, artifact gambar, upload dan audit. Default project bernama `default`.

Project lain dapat dibuat melalui POST /api/projects, lalu dipilih dengan header `X-Lab-Project` untuk HTTP atau `hello.project_id` untuk WS. Satu token owner mengakses semua project; project merupakan pemisahan data, bukan akun/otorisasi tenant terpisah.

Driver pgx menggunakan mode tanpa prepared statements berbasis koneksi agar cocok dengan pooler. Pool default SYNC 4 dan setiap proses/invocation worker 2 koneksi; idle minimum nol. Sesuaikan total pool dengan concurrency Lambda dan kapasitas Neon. [Dokumentasi pooler Neon](https://neon.com/docs/connect/connection-pooling).

File SQLite/data lama tidak dihapus dan tidak ditransfer otomatis. Jangan menganggap schema baru memuat histori sebelumnya. Gambar disimpan sebagai bytea di Neon agar dibaca SYNC/ASYNC tanpa filesystem bersama; untuk dataset media sangat besar, ekstensi object storage dapat ditambahkan kemudian.

## 2. Build artefak

Go 1.26+ diperlukan; go.mod memilih toolchain 1.26.9. Go mengunduh toolchain bila konfigurasi GOTOOLCHAIN mengizinkan.

```bash
go mod download
go test ./...
go vet ./...
go run ./cmd/package
```

| File | Target |
| --- | --- |
| `dist/async-lambda-arm64.zip` | ZIP siap upload; hanya executable `bootstrap` pada root, mode 0755 |
| `dist/bootstrap` | Linux ELF64 ARM64 statis, CGO disabled, tag lambda.norpc |
| `dist/arl-sync-linux-amd64` | Gateway untuk t3.micro x86_64 |
| `dist/arl-worker-linux-amd64` | Worker lokal/staging; tidak perlu dijalankan di EC2 production jika memakai Lambda |
| `dist/manifest.json` | SHA256 dan ukuran setiap artefak |

Build dan ZIP dibuat oleh Go, termasuk metadata permission Unix saat build di Windows. Packager memeriksa arsitektur ELF, tidak ada dynamic interpreter, serta struktur ZIP. ZIP tidak menyertakan .env, credentials, frontend atau database.

Untuk build SYNC sendiri langsung di EC2:

```bash
go build -trimpath -ldflags="-s -w" -o arl-sync main.go
```

Pola packaging mengikuti [dokumentasi Go Lambda](https://docs.aws.amazon.com/lambda/latest/dg/golang-package.html). `bootstrap` ini worker SQS, bukan handler HTTP publik.

## 3. Provision ASYNC Lambda + SQS

[deploy/async.template.json](deploy/async.template.json) membuat:

- Standard SQS dengan server-side encryption, HTTPS-only queue policy, DLQ dan Retain.
- Lambda runtime `provided.al2023`, architecture `arm64`, handler `bootstrap`.
- Timeout 900 detik, memory default 1024 MiB, reserved concurrency default 2, maksimum template 10.
- SQS batch 4/window 1 detik, maximum concurrency selaras dengan reserved concurrency, partial batch responses.
- Worker goroutine default 4/invocation dan pool database 2/invocation; GOMEMLIMIT 768 MiB.
- Visibility timeout 5401 detik; retention antrean kerja 4 hari, DLQ 14 hari.
- IAM worker hanya consume antrean ini dan menulis log group; log retention 14 hari.
- Managed policy khusus publish `sqs:SendMessage` untuk ditempel ke instance role EC2.

Contoh memakai bucket S3 privat yang sudah ada di region deployment. Tidak ada public Function URL/API Gateway untuk worker. Lambda tidak ditempatkan dalam VPC pada template agar endpoint Neon/provider publik dapat diakses tanpa NAT.

Buat JSON rahasia dari [deploy/runtime-secret.example.json](deploy/runtime-secret.example.json) pada lokasi privat di luar Git. Empat key wajib ada: `NEON_DB_URL`, `OPENROUTER_API_KEY`, `OPENAI_API_KEY` dan `GROQ_API_KEY`. Provider yang tidak dipakai boleh string kosong. Jangan memakai file contoh tanpa mengganti placeholder.

```bash
export AWS_REGION=ap-southeast-1
export CODE_BUCKET=YOUR-EXISTING-PRIVATE-BUCKET
export CODE_KEY=arl/async-lambda-arm64-VERSION.zip

aws s3 cp dist/async-lambda-arm64.zip "s3://$CODE_BUCKET/$CODE_KEY"
# Jalankan satu kali; gunakan ARN secret yang dihasilkan:
aws secretsmanager create-secret \
  --name arl/runtime \
  --secret-string file://PATH-TO-PRIVATE-runtime.json
export RUNTIME_SECRET_ARN=ARN-HASIL-CREATE-SECRET

aws cloudformation deploy \
  --template-file deploy/async.template.json \
  --stack-name arl-async \
  --capabilities CAPABILITY_IAM \
  --parameter-overrides \
    CodeBucket="$CODE_BUCKET" \
    CodeKey="$CODE_KEY" \
    RuntimeSecretArn="$RUNTIME_SECRET_ARN"

aws cloudformation describe-stacks \
  --stack-name arl-async \
  --query 'Stacks[0].Outputs'
```

Untuk update kode gunakan S3 key versi baru lalu deploy stack lagi. Template memakai dynamic reference Secrets Manager pada konfigurasi environment Lambda. Identity yang menjalankan CloudFormation perlu akses membaca secret (dan decrypt jika memakai customer-managed KMS). Worker membaca environment yang telah diresolve; role worker tidak membaca secret secara langsung. Rotasi secret memerlukan update konfigurasi/stack agar nilainya diperbarui.

Ambil QueueUrl dan SyncPublishPolicyArn dari outputs. Tempel managed policy publish itu ke instance role EC2 yang sudah Anda pakai. Berikan EC2 `ARL_SQS_QUEUE_URL` hasil output. SDK Go memakai default credential chain/instance profile; tidak perlu menaruh access key AWS statis dalam .env.

Jika menambah provider OpenAI-compatible dengan api_key_env khusus, tambahkan environment secret itu ke Lambda secara eksplisit; template bawaan mencakup tiga provider di atas. Koneksi `local` dalam Lambda harus menunjuk endpoint yang dapat diakses dari Lambda; localhost merujuk invocation tersebut, bukan komputer Anda atau EC2.

Provider API key cukup berada pada worker/Lambda. Saat refresh katalog, worker menyimpan status ketersediaan berupa boolean di PostgreSQL agar UI melalui SYNC dapat memilih provider tanpa menyalin secret ke EC2. Status configured berarti key tersedia, bukan hasil health check provider. Setelah mengubah secret worker, gunakan Refresh catalog pada UI untuk memperbarui status dan katalog; startup gateway juga menjadwalkan refresh harian.

Konfigurasi batch/visibility/partial failures mengikuti [SQS event source](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html) dan [penanganan kegagalan batch](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-errorhandling.html).

## 4. Jalankan SYNC pada EC2 t3.micro

Copy binary `dist/arl-sync-linux-amd64` ke direktori aplikasi sebagai `arl-sync`. Buat `.env` privat di direktori yang sama; file ini dibaca relatif working directory.

```dotenv
NEON_DB_URL=postgresql://USER:PASSWORD@YOUR-NEON-POOLER/neondb?sslmode=require
ARL_ADDR=127.0.0.1:8000
ARL_AUTH_TOKEN=YOUR-RANDOM-OWNER-TOKEN
ARL_ALLOWED_ORIGINS=https://YOUR-APP.vercel.app,https://YOUR-CUSTOM-FRONTEND
ARL_SQS_QUEUE_URL=https://sqs.REGION.amazonaws.com/ACCOUNT/QUEUE
AWS_REGION=ap-southeast-1
ARL_DB_MAX_CONNS=4
ARL_MAX_HTTP=48
ARL_MAX_WS=100
```

```bash
chmod 700 arl-sync
chmod 600 .env
GOMEMLIMIT=128MiB nohup ./arl-sync >sync.log 2>&1 &
echo $! >sync.pid
curl --fail http://127.0.0.1:8000/api/health
# Graceful stop dari direktori yang sama:
kill -TERM "$(cat sync.pid)"
```

Batas heap Go adalah soft limit, bukan pembatas RSS keras; upload/download gambar dan payload JSON tetap memakai memori. Gateway membatasi HTTP/WS dan dua multipart upload bersamaan; worker/analisis tidak dijalankan pada proses ini. Untuk uptime setelah reboot, Anda dapat membungkus binary yang sama dengan systemd; nohup di atas memenuhi mode manual.

Pasang reverse proxy TLS memakai [deploy/nginx.conf.example](deploy/nginx.conf.example), edit domain/certificate. Pertahankan upgrade WebSocket, SSE tanpa buffering, dan timeout proxy. Buka akses publik melalui HTTPS/WSS 443; port Go loopback 8000. Atur origin Vercel yang tepat pada ARL_ALLOWED_ORIGINS. Token owner diisi melalui halaman login; tidak pernah dimasukkan ke build frontend.

SYNC harus tetap hidup untuk koneksi WebSocket dan dispatcher outbox. Start listening berada di memori proses, default stopped setelah restart. Jalankan satu gateway aktif; template ini tidak menyediakan load balancer/multi-instance sinkronisasi listener.

## 5. Deploy frontend ke Vercel

Tetapkan **Root Directory = frontend**, build `npm run build`, output `dist`. Copy [deploy/vercel.json.example](deploy/vercel.json.example) menjadi `frontend/vercel.json` lalu ganti `YOUR-SYNC-DOMAIN` dengan hostname HTTPS gateway yang nyata. Rewrite `/api/*` menuju SYNC; fallback lainnya menuju index.html untuk React Router.

Set environment build Vercel:

```dotenv
VITE_LAB_WS_URL=wss://YOUR-SYNC-DOMAIN/api/agent-lab/ws
```

Tidak ada NEON_DB_URL, provider API key, owner token atau AWS credential dalam VITE_*.

Request HTTP frontend memakai path /api pada origin Vercel; external rewrite meneruskannya ke SYNC. Cookie login tetap pada origin UI. Browser demo meminta ticket sekali pakai melalui API ini, lalu tersambung WSS langsung ke gateway. Backend yang diuji memakai bearer melalui LAB_TOKEN. TLS gateway dan origin allowlist harus sudah benar.

[External rewrites Vercel](https://vercel.com/docs/routing/rewrites) meneruskan HTTP. [Vercel Functions tidak menjadi server WebSocket](https://vercel.com/kb/guide/do-vercel-serverless-functions-support-websocket-connections); URL WSS harus langsung ke gateway.

[Batas proxy eksternal Vercel](https://vercel.com/docs/limits) adalah 120 detik. Penantian job HTTP gateway dibatasi 110 detik; job yang lebih panjang tetap berada di antrean. SSE campaign dapat reconnect dari cursor Last-Event-ID. Playground merupakan stream satu request; panggilan yang melampaui batas proxy dapat terputus di UI meskipun worker masih menyelesaikan panggilan. Untuk pengujian latency panjang gunakan campaign/trace durable dan baca hasilnya kembali.

Untuk lokal, `frontend/vite.config.ts` mem-proxy /api dan WS ke localhost:8000. `frontend/.env.example` menunjukkan URL WSS publik opsional.

## 6. Jalankan backend lokal tanpa AWS

```bash
# .env berisi NEON_DB_URL; ARL_SQS_QUEUE_URL kosong
go run ./cmd/admin init --apply
# Terminal pertama:
go run ./cmd/worker
# Terminal kedua:
go run main.go
# Terminal frontend:
cd frontend
npm ci
npm run dev
```

Atau `docker compose up --build` menjalankan SYNC dan worker dengan image Go yang sama, read-only filesystem dan .env. Docker tidak menjalankan migration otomatis. Compose mengosongkan QueueURL agar worker lokal yang mengambil job. Jangan jalankan worker lokal production bersamaan dengan Lambda kecuali memang ingin keduanya berbagi pekerjaan.

Buka Agent lab → Start listening. Untuk external service, gunakan kontrak [connection.md](connection.md). Demo end-to-end tanpa model berbayar:

```bash
export LAB_WS_URL=ws://localhost:8000/api/agent-lab/ws
export STAGE=dev
export LAB_REQUIRED=true
export LAB_TOKEN=YOUR-OWNER-TOKEN
go run ./examples/go-agent
```

## 7. Delivery, retry, biaya dan batas

Antrean database menjadi durable outbox. SYNC mengirim ID job ke SQS hanya setelah transaksi commit; publish gagal meninggalkan job untuk pengiriman berikutnya. Worker melakukan atomic lease claim, heartbeat, bounded goroutines dan partial batch failure; semua goroutine milik handler ditunggu sebelum invocation selesai. Worker lokal memakai proses/engine yang sama, tetapi polling database.

Model calls tetap paralel, sedangkan job dengan penggunaan memori besar seperti import, decode gambar, analisis dan report dibatasi satu pada setiap proses/invocation. Invocation Lambda yang berbeda tetap berjalan paralel. Runtime menurunkan batas heap ke maksimal 75% memory Lambda dan menghormati GOMEMLIMIT yang lebih kecil.

SQS dan provider tidak memberikan transaksi end-to-end exactly-once. Deduplikasi job, ID campaign/trial deterministik dan checkpoints mencegah replay internal biasa. Jika proses mati di tengah panggilan berbayar, recovery tidak mengirim ulang panggilan itu secara diam-diam: trial menjadi interrupted, biaya unknown, campaign memerlukan reconciliation. Periksa provider sebelum resume eksplisit. Cadangan biaya unknown tidak diubah menjadi nol; budget bisa tetap tertahan karena exposure belum diketahui. Resume ditolak selama masih ada active calls. Pembatalan menghentikan admission/retry baru, sedangkan panggilan yang sudah berjalan diselesaikan dan dicatat.

Satu job dicoba paling banyak empat kali karena kegagalan infrastruktur; retry provider mengikuti plan maksimal enam retry. Lease 12 menit diperpanjang setiap 25 detik; dispatcher memulihkan lease kedaluwarsa. Job failed terlihat melalui GET /api/jobs/{id}; redrive DLQ tidak otomatis mengubah job failed menjadi queued. Inspeksi dan rekonsiliasi DB diperlukan untuk kegagalan terminal. Scheduler/worker dapat menunda trial saat concurrency penuh tanpa menghabiskan retry budget.

Satu provider attempt dibatasi maksimum empat menit. Jika konfigurasi timeout_s lebih tinggi, timeout efektif tetap maksimum ini; rangkaian retry/backoff yang melewati deadline invocation dapat berakhir interrupted/unknown. Lambda maksimum 15 menit. Durasi panjang sebaiknya dibagi menjadi job/fase lebih kecil. Local service yang hanya ingin ditrace tetap menjalankan agent loop-nya sendiri; lab tidak menjalankan tool atas perintah WS.

HTTP yang menunggu job memiliki batas 110 detik dan header X-Lab-Job-ID. Jika request frontend timeout, job tetap berjalan. Untuk create/launch/rerun/resume melalui API, kirim Idempotency-Key yang sama saat retry agar pekerjaan tidak diduplikasi; penggunaan key yang sama dengan payload berbeda ditolak. Endpoint GET results/report dapat dibaca ulang. Playground disconnect menghentikan target yang belum mulai; provider call yang sudah dimulai dapat tetap selesai dan dikenai biaya.

Batas: 48 arms, 50.000 trial, 100.000 assessments per revision; 1 MiB/frame WS, 20.000 events dan 32 MiB payload/run. Gambar maksimum 20 MiB/40 juta pixel; staged dataset maksimum 100 MiB dan body multipart total maksimum 128 MiB. Full report dibatasi 48 MiB untuk menjaga memory. Audio/video/file merupakan metadata evidence, belum binary storage/player. Belum ada retention otomatis; kelola arsip/cleanup trace, staged uploads, jobs, stream events dan report sesuai kebutuhan. Jangan menghapus row yang masih direferensikan atau job yang belum terminal.

## 8. Verifikasi dan tests

Unit tests tidak memerlukan database. Integration tests hanya mengambil ARL_TEST_DB_URL, tidak pernah fallback ke NEON_DB_URL. Gunakan database khusus lokal yang sudah menerima sql/init.sql:

```bash
docker run --detach --name arl-test \
  --publish 127.0.0.1:55439:5432 \
  --env POSTGRES_PASSWORD=arl-test-only \
  --env POSTGRES_DB=arl_test postgres:17-alpine

export ARL_TEST_DB_URL='postgres://postgres:arl-test-only@127.0.0.1:55439/arl_test?sslmode=disable'
psql "$ARL_TEST_DB_URL" -v ON_ERROR_STOP=1 -f sql/init.sql
go test ./... -count=1 -timeout 180s
go vet ./...
# Linux/macOS dengan C compiler:
go test -race ./... -count=1 -timeout 240s
cd frontend
npm ci
npm run build
```

Tests memberi setiap kasus project unik dan membersihkan data project itu. Remote test DB memerlukan opt-in ARL_ALLOW_REMOTE_TEST_DB=true; jangan pakai database produksi. Linux Docker dapat dipakai untuk race detector pada host Windows.

Suite mencakup concurrent budget admission/lease/dedup, scheduler, scorer/statistik, provider streaming, fake campaigns, retry/cancel/rescore/budget, report checksum, imports/artifact, auth, WS resume/duplicate dan single-use browser tickets. Headless frontend protocol test opsional terhadap lab lokal tanpa auth: `node frontend/tests/agent_lab_demo.mjs http://127.0.0.1:8000`.

Validasi template lokal: `cfn-lint deploy/async.template.json`. Provisioning AWS dan koneksi deployment Neon/Vercel tetap dilakukan dengan akun/hostname Anda; build dan tests lokal tidak membuktikan IAM, DNS, TLS atau jaringan akun deployment.
