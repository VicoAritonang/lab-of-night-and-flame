# Menghubungkan backend ke Agent Reliability Lab

Dokumen integrasi protokol **`arl/1`**. Bahasa/framework backend bebas: kirim JSON melalui WebSocket. Lab mengamati pekerjaan yang dijalankan backend Anda, menyimpan evidence di Neon PostgreSQL, dan menyediakan visualisasi serta pemeriksaan hasil. Backend tetap menjalankan model, retrieval, dan tool-nya sendiri.

## Mulai cepat

1. Jalankan lab (`docker compose up -d`); buka **Agent lab** (`/agent-lab`).
2. Image Docker menerima agent langsung (`ARL_LISTEN_ON_START=true`). Pada deployment tanpa opsi itu, klik **Start listening**; listener dimulai dalam keadaan berhenti setelah restart.
3. Tambahkan ke environment **backend yang akan diuji**:

```dotenv
LAB_WS_URL=ws://localhost:8000/api/agent-lab/ws
STAGE=dev
# Jika lab dilindungi ARL_AUTH_TOKEN, isikan nilai yang sama:
LAB_TOKEN=
# Opsional, project tujuan dan mode strict untuk integration test:
LAB_PROJECT_ID=default
LAB_REQUIRED=false
```

4. Sebelum setiap job/request, jika `STAGE=dev` dan `LAB_WS_URL` terisi, coba koneksi dengan timeout pendek lalu kirim `hello`. Selain `dev`, jangan menghubungkan instrumentation ini.
5. Tunggu `hello_ack`, kirim `run.start`, event setiap fase, kemudian `run.end`. Tunggu ACK setiap event atau korelasikan ACK menggunakan `event_id`.
6. Buka trace dari daftar **Captured runs**. Tombol **Run protocol demo** mendemonstrasikan protokol yang sama tanpa panggilan AI berbayar.

Alamat harus dapat dijangkau **dari backend pemanggil**: Docker Compose satu network memakai `ws://sync:8000/api/agent-lab/ws`; container menuju host biasanya memakai `host.docker.internal`; host menuju port Docker yang dipublikasikan memakai `localhost:8000`. Compose bawaan mempublikasikan port ke loopback. Untuk mesin lain atur jaringan/publikasi port sesuai kebutuhan. Gunakan `wss://` saat berada di belakang HTTPS. Vite development port 5173 sudah meneruskan `/api` termasuk WebSocket ke port 8000.

Default integrasi sebaiknya **fail-open**: lab tidak tersedia berarti job bisnis tetap berjalan, dengan log bahwa trace tidak lengkap. Untuk integration test yang harus punya seluruh evidence, gunakan mode strict dan gagalkan test bila koneksi/ACK gagal. `LAB_REQUIRED` merupakan opsi adapter contoh, bukan field handshake server.

## Fitur dan hubungan dengan lab yang sudah ada

| Fitur | Evidence / cara memakai |
| --- | --- |
| Input dan output multimodal | `input` / `output` dengan `data.content`; teks, JSON, gambar lokal tervalidasi, serta metadata referensi audio/video/file |
| Pencarian knowledge graph | `graph.node`, `graph.edge`, `graph.visit`; node klik, arah edge, jumlah kunjungan, score, context terpilih, jalur kunjungan dan replay |
| Retry retrieval dan agent | `retrieval.retry`, `agent.retry`, atau `retry`; kirim setiap retry, bukan hanya jumlah akhir |
| Agent loop | `span.start` / `span.end`, `model.request`, `model.response`, `model.chunk`, tool call/result; parent span untuk nesting dan operasi paralel |
| Service umum/router | `hello.lab=generic` atau `router`, fase kustom, event kustom; dirender di flow fase dan timeline/raw inspector yang sama |
| Biaya dan token | `cost.record` per operasi; reported / estimated / unknown; rate USD per sejuta token; termasuk retry dan kegagalan |
| Pemeriksaan otomatis | Default **Listener settings** atau `run.start.data.checks`; biaya, latency, retry, node wajib, tool terlarang dan substring output |
| Evaluator eksternal | Event `evaluation` dengan boolean `passed` dan evidence; backend bebas memakai evaluator sendiri |
| Penyimpanan dan replay | ACK setelah commit; session/run terpisah; replay slider, pencarian payload, pagination timeline dan ekspor JSON |
| Model comparison, Router lab, datasets, scorers, campaigns, Playground | Fitur lama tetap berjalan melalui HTTP API; bukan perintah untuk menjalankan backend Anda melalui WS |

Halaman Agent lab diperbarui melalui HTTP polling (~1–2 detik); backend service mengirim evidence melalui WebSocket. Graph menampilkan maksimal 120 node sekaligus agar tetap terbaca; seluruh node/event tetap tersimpan dan tersedia melalui timeline/ekspor. Filter retrieval span memisahkan pencarian yang berjalan paralel. Replay mempengaruhi graph, input/output, spans, dan baris ledger; angka ringkasan serta verdict selalu menggambarkan keseluruhan trace.

## Autentikasi

Jika lab memiliki `ARL_AUTH_TOKEN`, setiap koneksi WS dan HTTP API terproteksi harus membawa:

```http
Authorization: Bearer <nilai ARL_AUTH_TOKEN>
```

Gunakan header, bukan query string URL. UI memakai cookie login yang sudah ada. Endpoint WebSocket memeriksa token sebelum menerima koneksi. Browser demo meminta ticket sekali pakai melalui POST /api/agent-lab/ticket menggunakan cookie UI; ticket berlaku 30 detik, kemudian dipakai membuka koneksi WSS langsung ke SYNC. Ini mendukung frontend Vercel tanpa menaruh owner token pada environment frontend. Backend eksternal memakai header bearer. Jangan memasukkan provider API key atau token ke event. Resume token adalah rahasia session dan disimpan client secara privat; server hanya menyimpan hash-nya.

## Handshake: backend menyatakan lab dan fase

Setiap koneksi diawali **satu text frame UTF-8 JSON**:

```json
{
  "type": "hello",
  "protocol": "arl/1",
  "service": "my-backend/support-agent",
  "stage": "dev",
  "lab": "agent",
  "phases": ["input", "retrieval", "agent", "output"],
  "capabilities": ["knowledge_graph", "tools", "images", "costs"],
  "metadata": {"build": "git-sha", "framework": "your-framework", "test_suite": "smoke"},
  "project_id": "default"
}
```

| Field | Kontrak |
| --- | --- |
| `type`, `protocol`, `service`, `stage` | Wajib; type `hello`, protocol `arl/1`, service 1–200 karakter, stage hanya `dev` |
| `lab` | `agent` (default), `router`, atau `generic`; menentukan label/profile trace |
| `phases` | Default input/retrieval/agent/output; 1–32 nama unik, masing-masing 1–80 karakter; semua phase event harus dideklarasikan di sini |
| `capabilities` | Daftar kemampuan yang diumumkan backend, disimpan untuk inspeksi; bukan permintaan eksekusi tool atau jaminan instrumentation lengkap |
| `metadata` | JSON bebas, misalnya build, versi agent, dataset key, configuration hash; jangan kirim rahasia |
| `project_id` | Opsional, default `default`; project harus sudah dibuat. HTTP memakai header `X-Lab-Project`. Ini pemisahan data satu owner, bukan autentikasi tenant terpisah. |
| `session_id`, `resume_token` | Hanya untuk melanjutkan session yang sudah ada; harus dikirim berpasangan |

Response:

```json
{
  "type": "hello_ack",
  "protocol": "arl/1",
  "session_id": "session_...",
  "resume_token": "simpan-secara-privat",
  "lab": "agent",
  "phases": ["input", "retrieval", "agent", "output"],
  "max_message_bytes": 1048576,
  "max_events_per_run": 20000,
  "max_trace_bytes": 33554432,
  "heartbeat_seconds": 20,
  "features": ["graph", "timeline", "spans", "media", "costs", "checks", "resume", "export"]
}
```

`resume_token` hanya dikembalikan saat membuat session. Client yang resume harus memakai service/lab/phases asli. Satu session memiliki satu koneksi aktif; koneksi paralel memakai session berbeda. Satu koneksi dapat membawa banyak run yang diinterleave selama `run_id` berbeda. Untuk service umum misalnya gunakan `lab=generic`, `phases=["receive","validate","database","publish"]` lalu event kustom `validation.result`, `database.query`, dan sebagainya. Graph renderer bekerja berdasarkan jenis event sehingga graph dapat muncul pada fase mana pun.

## Envelope setiap event

```json
{
  "type": "event",
  "event_id": "uuid-event-unik",
  "run_id": "request-id-atau-job-id",
  "event": "model.request",
  "phase": "agent",
  "span_id": "model-attempt-1",
  "parent_span_id": "agent-iteration-1",
  "timestamp": 1792368000.123,
  "data": {
    "model": "provider/model",
    "content": [{"type":"text","text":"Prompt yang benar-benar dikirim"}]
  }
}
```

Wajib: `type`, `event_id` (1–128 karakter), `run_id` (1–128), dan `event`. `event` maksimal 100 karakter dan cocok dengan `^[a-z][a-z0-9_.-]*$`. `phase` default `agent`, tetapi selalu sebutkan secara eksplisit ketika memakai fase kustom. `span_id`/`parent_span_id` opsional, maksimal 128 karakter; wajib `span_id` untuk span.start/end. `timestamp` opsional, Unix seconds dari client. `data` selalu object JSON. Envelope menolak field yang tidak dikenali; extension taruh di `data`/`metadata`.

ACK setelah penyimpanan:

```json
{"type":"ack","event_id":"uuid-event-unik","run_id":"job-1","trace_id":"trace_...","ordinal":42,"duplicate":false}
```

`run_id` milik backend; `trace_id` milik lab untuk URL HTTP/UI. `ordinal` merupakan urutan penerimaan server yang naik global, bukan nomor berurutan per-run; bisa ada gap karena run lain. Untuk operasi paralel gunakan span/parent dan timestamp, jangan menyimpulkan urutan kausal hanya dari waktu kedatangan WS.

## Data per fase dan jenis event

Semua contoh di tabel adalah isi `data`, dibungkus envelope di atas. Kecuali dinyatakan wajib, field merupakan konvensi yang direkomendasikan dan tetap tersimpan sebagai JSON.

| Event | Data minimum / rekomendasi | Visualisasi |
| --- | --- | --- |
| `run.start` | `name`, opsional `checks`, `dataset_key`, `test_case`, `expected`, `tags`; harus event pertama suatu run | Run baru, checks dibekukan |
| `input` | `content:[...]`, opsional `request`, `user_context` yang sudah disanitasi | Panel input, gambar dan inspector |
| `span.start` | Wajib envelope `span_id`; `name`, `iteration`, `query`; opsional parent span | Root/child agent loop, retrieval dan operasi paralel |
| `graph.node` | Wajib `node_id`; `label`, `kind`, `properties` | Definisi node; kirim juga kandidat yang tidak dikunjungi untuk menunjukkan ruang pencarian |
| `graph.edge` | Wajib `source`, `target`; `label`, `properties` | Edge terarah; satu hubungan visual per pasangan source/target |
| `graph.visit` | Wajib `node_id`; `from_node`, `depth`, `attempt`, `score`, `selected`, `retrieval_id` | Jumlah kunjungan, edge yang dilewati, urutan kunjungan, context terpilih |
| `retrieval.retry` | `attempt`, `reason`, `backoff_ms`, `query` | Retry marker dan penghitung |
| `retrieval.result` | `node_ids`, `content`, `documents`, `scores`, `query`, `strategy` | Hasil context sebagai evidence timeline; bisa digunakan tanpa graph untuk vector/full-text search |
| `model.request` | `model`, `provider`, `iteration`, `content`, `parameters`; gunakan span ID unik per panggilan/attempt | Prompt/request dan hubungan ke loop |
| `model.response` | `model`, `content`, `tokens_in`, `tokens_out`, `finish_reason`, `request_id`, `duration_ms` | Respons, penggunaan dan raw inspector |
| `model.chunk` | `content:[{"type":"text","text":"delta"}]`, opsional `chunk_index` | Delta terurut di timeline; kirim juga respons/output final lengkap untuk panel output dan checks |
| `tool.call` | Wajib `tool`, `call_id`; `arguments`; call_id unik per attempt dalam run | Tool exchange, argumen, relasi span |
| `tool.result` | Wajib `call_id` yang cocok; `status`, `result`, `content`, `duration_ms`, `error` | Pasangan hasil tool; gambar bisa berada di content |
| `agent.retry` / `retry` | `attempt`, `reason`, referensi operasi | Penghitung retry; tiap attempt tetap punya model/tool dan cost record sendiri |
| `span.end` | Envelope `span_id` yang sama; `duration_ms`, `status` | Durasi operasi; tanpa duration_ms memakai selisih waktu server |
| `cost.record` | Wajib `billing_id`; detail di bagian biaya | Ledger dan token accounting |
| `output` | `content:[...]`, opsional `structured_output`, `citations`, `artifacts` | Panel output; substring check membaca semua text part dari event output |
| `evaluation` | Wajib `name`, `passed` boolean; opsional `score`, `evidence`, `evaluator_version` | Verdict evaluator eksternal dan evidence |
| `error` | `category`, `message`, `retryable`, referensi operasi/span | Evidence error pada timeline; tidak otomatis menutup run |
| `run.end` | `status`: completed/failed/cancelled (default completed); `duration_ms`, `error` | Menutup run secara final; durasi backend lebih tepat saat event dikirim ulang dari queue |
| Event kustom, mis. `route.decision` | JSON bebas: `requested_model`, `resolved_model`, `reason`, `candidates`, dll. | Timeline/inspector dan fase kustom; tidak otomatis masuk chart kampanye Router lab lama |

`duration_ms`, `attempt`, `iteration`, `depth`, `tokens_in`, `tokens_out` harus angka finite non-negatif; semua kecuali duration_ms harus integer. `score` harus angka finite dan boleh negatif, misalnya cosine similarity. `name`, `label`, `model`, `category`, `reason`, `status`, `from_node`, `query`, `tool`, `call_id`, jika ada, harus string. Data lain dapat berupa JSON bebas. Node ID dikoaleskan dalam satu run; beri namespace berbeda bila ID dari retrieval berbeda tidak merepresentasikan entitas yang sama. Gunakan span_id berbeda untuk memisahkan pencarian paralel pada filter graph.

Contoh jalur dua hop dan retry:

```json
{"event":"graph.node","phase":"retrieval","data":{"node_id":"q1","label":"Query","kind":"query"}}
{"event":"graph.node","phase":"retrieval","data":{"node_id":"doc:17","label":"Policy","kind":"document"}}
{"event":"graph.edge","phase":"retrieval","data":{"source":"q1","target":"doc:17","label":"matches"}}
{"event":"graph.visit","phase":"retrieval","data":{"node_id":"q1","attempt":1,"depth":0}}
{"event":"graph.visit","phase":"retrieval","data":{"node_id":"doc:17","from_node":"q1","attempt":1,"depth":1,"score":0.42}}
{"event":"retrieval.retry","phase":"retrieval","data":{"attempt":2,"reason":"Confidence below threshold"}}
{"event":"graph.visit","phase":"retrieval","data":{"node_id":"doc:17","from_node":"q1","attempt":2,"depth":1,"score":0.93,"selected":true}}
```

Contoh di atas disingkat: tambahkan `type`, `event_id`, `run_id`, dan `span_id` setiap mengirim melalui WS. Jangan hanya mengirim graph final: kirim setiap kunjungan/retry saat terjadi agar replay dan hitungan benar.

## Gambar dan media

Gunakan penyimpanan artifact gambar yang sudah tersedia. Upload HTTP multipart terlebih dahulu:

```bash
curl -H "Authorization: Bearer YOUR_LAB_TOKEN" \
  -F "file=@screenshot.png" http://localhost:8000/api/artifacts
```

Respons berisi `digest`, `mime`, `width`, `height`, `bytes`. Gunakan digest sha256 tersebut:

```json
{"content":[
  {"type":"text","text":"Inspect screenshot ini"},
  {"type":"image","artifact":"<64-karakter-sha256-digest>","alt":"Screenshot halaman pembayaran"},
  {"type":"json","value":{"expected_status":"success"}}
]}
```

Gambar dapat dikirim pada input, retrieval.result, model response, tool.result atau output. Format yang diterima: PNG/JPEG/WEBP/GIF; maksimal 20 MiB dan 40 juta pixel; byte asli dipertahankan, deduplikasi berbasis hash. Server memvalidasi artifact sudah ada sebelum menerima event. Jangan mengirim base64 atau binary image melalui WS; batas frame JSON 1 MiB.

Content type `audio`, `video`, `file` dapat membawa metadata seperti `name`, `mime`, `uri`, `duration_ms`, `transcript`; ditampilkan sebagai metadata/raw evidence. Versi ini belum menyimpan file selain gambar dan belum menyediakan audio/video player. Lab tidak mengunduh URL eksternal otomatis. Ekspor trace berisi referensi gambar, bukan seluruh byte gambar; ambil byte lewat `GET /api/artifacts/{digest}` untuk arsip portabel.

## Biaya, estimasi dan coverage

Satu operasi billable = satu `cost.record`. **`billing_id` harus cocok dengan `model.request.span_id` (atau event_id request bila tanpa span), atau `tool.call.data.call_id`.** Operasi kustom seperti retrieval berbayar boleh memakai billing_id unik tersendiri. Jangan menduplikasi biaya yang sama pada model.response dan cost.record; hanya cost.record yang masuk ledger/token totals. Selalu catat panggilan gagal/retry yang dikenai biaya. Operasi gratis tetap mengirim `amount_usd:0` agar ada evidence eksplisit.

Reported cost:

```json
{"billing_id":"model-attempt-1","model":"provider/model","tokens_in":1500,"tokens_out":300,"amount_usd":0.0042,"source":"reported"}
```

Estimasi dari rate manual (USD per 1 juta token):

```json
{"billing_id":"model-attempt-2","model":"provider/model","tokens_in":1500,"tokens_out":300,"pricing":{"input_per_million":1,"output_per_million":3}}
```

Tanpa amount, server memakai pricing pada event, lalu fallback exact model name di **Listener settings**. Estimasi memerlukan kedua token count, termasuk `0` bila tepat nol. Formula `(tokens_in*input_rate + tokens_out*output_rate)/1_000_000`. Rate dan hasil disimpan saat ingest sehingga perubahan konfigurasi tidak mengubah histori. Jika amount/token/rate belum diketahui, kirim record dengan billing_id tanpa amount; server memberi `source:unknown`. Amount eksplisit dapat diberi `source:estimated` jika dihitung backend. Tidak ada konversi mata uang otomatis.

Ringkasan menunjukkan **known subtotal**, jumlah estimated dan unknown. Model requests/tool calls yang tidak mempunyai billing record dihitung sebagai unreported/unknown, bukan dianggap gratis. Cost gate yang berada di bawah cap tetapi memiliki biaya unknown menghasilkan `insufficient_evidence`. Known subtotal di atas cap langsung gagal. Coverage hanya dapat diperiksa untuk operasi yang Anda instrumentasikan; lab tidak bisa mendeteksi panggilan provider tersembunyi. Semua budget merupakan **pemeriksaan pasca/pada pengamatan**, bukan pembatas pengeluaran backend; backend harus menegakkan spending limit sendiri.

`billing_id` unik per run. Untuk resend, gunakan event_id/payload yang sama. Record biaya append-only; kirim satu biaya final per operasi. Jika belum final, simpan progres di custom event dan tunggu final cost.record. Unknown cost yang sudah terlanjur dicatat tidak dapat diganti dengan event baru untuk billing_id sama dalam versi ini.

## Checks dan hasil pengujian

Konfigurasi contoh untuk **Listener settings** atau `POST /api/agent-lab/config`:

```json
{
  "pricing": {"provider/model":{"input_per_million":1,"output_per_million":3}},
  "checks": {
    "max_cost_usd": 0.1,
    "max_latency_ms": 10000,
    "max_retries": 2,
    "output_contains": ["source:"],
    "required_nodes": ["policy:refund"],
    "forbidden_tools": ["payments.charge"]
  }
}
```

`run.start.data.checks` meng-override field default secara parsial. Null menonaktifkan batas numerik; array kosong menonaktifkan check daftar. Konfigurasi dibekukan per run. Output substring bersifat case-sensitive dan membaca text content pada event `output`, bukan delta/chunk. Verdict `pass` hanya jika run completed, ada checks, dan seluruh check lulus. `fail` jika check gagal atau status run failed/cancelled. Tanpa checks, masih running, unknown cost, output yang belum dikirim, atau run interrupted menghasilkan `insufficient_evidence` kecuali ada bukti kegagalan. Custom evaluation dengan passed=false juga menggagalkan run; nilai score saja tidak menggantikan boolean passed.

Lab mencatat **execution status** terpisah dari **test verdict**. `run.end.status=completed` tidak berarti jawaban benar. Untuk evaluasi semantik, grounding, regresi dataset atau tool correctness, jalankan evaluator di backend dan kirim evaluation beserta evidence. Buat run terpisah untuk setiap test case/varian/repetisi dan beri identitas di metadata/data run.start. Ekspor dapat dikonsumsi CI melalui HTTP.

## Reconnect, replay, timeout, dan error

- Handshake harus tiba dalam 30 detik. Setelah handshake, kirim `{"type":"ping"}` setiap 20 detik jika tidak ada event; response `{"type":"pong","timestamp":...,"listening":true}`. Tanpa frame selama 90 detik koneksi ditutup.
- Listener stop menutup koneksi dengan code 1012. Listener restart dapat menerima resume. Run yang sedang berjalan tidak ditutup hanya karena koneksi terputus; pekerjaan bisa lanjut setelah reconnect.
- Reconnect membawa session_id/resume_token, service/lab/phases yang sama. Resend event yang belum mendapat ACK memakai **event_id dan payload asli**, termasuk timestamp. ACK `duplicate:true` berarti sudah tersimpan; tidak menambah biaya/kunjungan.
- Event ID unik pada seluruh session. Reuse untuk payload berbeda menghasilkan event_conflict. Run ID unik dalam session; service lain bisa memakai run ID sama pada session berbeda.
- `run.end` final: event baru setelahnya ditolak. Resend exact event lama tetap mendapat ACK duplicate. Untuk pekerjaan baru pakai run_id baru.
- Restart SYNC mengembalikan listener ke stopped. Aktifkan kembali listener, lalu resume session. Session, trace, event dan hash resume token berada di PostgreSQL; lease koneksi lama habis paling lambat sekitar 50 detik. Disconnect/restart tidak otomatis menutup run: kirim run.end dari backend untuk menyatakan hasil final.
- Maksimal 20.000 event dan total payload 32 MiB per run; setelah cap, run.end tetap diterima. Batasi/kelompokkan streaming token menjadi chunk yang lebih besar. Frames maksimal 1.048.576 byte; binary frames ditolak. JSON tidak menerima NaN/Infinity.
- Server menyimpan event setelah commit PostgreSQL. ACK bukan instruksi menjalankan tool. Client sebaiknya memiliki queue terbatas dan backoff, bukan memblokir request bisnis tanpa batas.

Format error:

```json
{"type":"error","code":"unknown_phase","message":"phase was not declared in the handshake","event_id":"..."}
```

| Code | Tindakan client |
| --- | --- |
| `not_listening` | Klik Start listening; reconnect dengan backoff |
| `validation_error`, `invalid_message`, `invalid_data`, `invalid_phases`, `unknown_phase` | Perbaiki schema/payload; jangan retry payload salah terus menerus |
| `invalid_resume`, `session_mismatch`, `session_busy` | Pakai token/profile asli; tunggu koneksi lama ditutup atau buat session baru |
| `run_not_started`, `run_exists`, `run_closed` | Periksa lifecycle dan run_id |
| `event_conflict`, `billing_conflict` | Perbaiki ID; resend harus exact, jangan menghitung biaya dua kali |
| `message_too_large`, `trace_limit` | Kurangi payload/event; upload image lewat HTTP; akhiri run dan segmentasi pekerjaan berikutnya |
| `missing_artifact` | Upload gambar ke lab ini terlebih dahulu |

Handshake invalid mengirim error lalu close 1008; token auth salah ditolak sebelum accept. Timeout close 1001. Setelah validation error pada koneksi yang sudah handshake, koneksi tetap dapat menerima event valid.

## SDK resmi (Go, Python, TypeScript)

| Bahasa | Install | Lokasi |
| --- | --- | --- |
| Python ≥ 3.9 | `pip install arl-lab` | [sdk/python](sdk/python) |
| TypeScript / Node ≥ 22, Bun, Deno | `npm install arl-lab` | [sdk/typescript](sdk/typescript) |
| Go ≥ 1.23 | `go get github.com/VicoAritonang/lab-of-night-and-flame/sdk/go` | [sdk/go](sdk/go) |

Ketiganya memakai kontrak yang sama: no-op di luar `STAGE=dev`, fail-open secara default (`LAB_REQUIRED=true` untuk test strict), satu request menunggu satu ACK (aman dipanggil paralel), heartbeat 20 detik, resend envelope identik saat ACK hilang, dan reconnect otomatis bila handler replay terdaftar. Helper tingkat tinggi: `run.input`, `run.snapshot`, `run.model_call`/`ModelCall`, `run.tool`, `run.output`, `run.end`. Contoh agent lengkap: [examples/python-agent](examples/python-agent/agent.py) dan [examples/go-agent](examples/go-agent/main.go).

Session/resume token disimpan dalam memori client; ini bukan durable outbox lintas proses. Bahasa lain cukup mengikuti kontrak JSON di dokumen ini.

## State snapshot, LLM step, dan replay (Workbench)

Workbench bekerja seperti Postman untuk agent: trace yang tertangkap dijadikan **scenario** yang bisa diedit (input, state, model, system prompt, parameter, checks) lalu dikirim ulang. Ada dua mode:

| Mode | Yang dijalankan | Syarat dari agent |
| --- | --- | --- |
| **Agent replay** | Lab mengirim frame `replay` ke agent yang terhubung; agent menjalankan ulang seluruh alurnya dengan nilai yang sudah diubah dan menghasilkan trace baru yang tertaut | Capability `replay` (otomatis bila SDK punya handler replay) |
| **LLM step** | Lab memanggil provider langsung (OpenRouter/OpenAI/Groq/lokal/fake) dengan request yang tertangkap | `model.request` membawa `data.request` |

### Event `state.snapshot`

Kirim di awal run; snapshot pertama menjadi titik awal replay.

```json
{"type":"event","event":"state.snapshot","phase":"input","run_id":"job-1","event_id":"...",
 "data":{"state":{"memory":["..."],"user_tier":"pro"},
         "config":{"model":"openai/gpt-4o-mini","system_prompt":"You are...","params":{"temperature":0.2}}}}
```

`state` dan `config` harus object. Isi bebas: apa pun yang dibutuhkan agent untuk menjalankan ulang pekerjaan yang sama.

### `model.request` yang bisa dikirim ulang

```json
{"event":"model.request","phase":"agent","span_id":"model-1",
 "data":{"model":"openai/gpt-4o-mini","provider":"openrouter",
         "request":{"model":"openai/gpt-4o-mini","temperature":0.2,"messages":[{"role":"system","content":"..."},{"role":"user","content":"..."}],"tools":[]}}}
```

`data.request` adalah payload chat OpenAI-compatible (object). Pesan `system` menjadi *System prompt* yang bisa diedit; sisa pesan menjadi *Messages*; `tools`/`tool_choice` ikut diteruskan.

### Frame `replay` (server → agent)

```json
{"type":"replay","replay_id":"wbr_...","scenario_id":"scn_...","source_trace_id":"trace_...","name":"Support question",
 "input":"Tell me about shipping","state":{...},"config":{"model":"...","system_prompt":"...","params":{...}},"checks":{...}}
```

Frame ini bisa datang kapan saja di antara ACK; client harus membaca socket di goroutine/thread terpisah dan membedakannya dari reply berdasarkan `type`. Agent lalu:

1. Mengirim `{"type":"replay.status","replay_id":"wbr_...","status":"started"}` (server membalas `{"type":"ack","replay_id":...}`).
2. Menjalankan pekerjaan dengan nilai dari frame dan mengirim run baru yang `run.start.data.replay = {"replay_id":"wbr_...","source_trace_id":"trace_...","scenario_id":"scn_..."}`; checks dari frame digabung ke `run.start.data.checks`.
3. Mengirim `replay.status` `completed`, atau `failed` dengan `error`, atau `rejected` bila tidak punya handler.

Status run Workbench diturunkan dari keduanya: kegagalan handler atau trace tidak pernah tertutupi; `completed` hanya bila trace selesai `completed` (atau handler selesai tanpa trace).

HTTP: `GET/POST /api/workbench/scenarios`, `GET/POST/DELETE /api/workbench/scenarios/{id}`, `POST /api/workbench/from-trace {"trace_id","step"?,"save"?}`, `POST /api/workbench/run {"scenario_id"?,"scenario"?,"mode":"agent|llm","session_id"?}`, `GET /api/workbench/runs?scenario_id=`, `GET /api/workbench/runs/{id}`, `GET /api/workbench/agents`.

## HTTP API dan fitur lab lain

Semua URL di bawah relatif terhadap origin lab, dengan bearer token bila dikonfigurasi. Indeks HTTP tersedia di `/api/docs` dan `/api/openapi.json`; bentuk payload mengikuti halaman frontend dan contoh dalam dokumen ini; handshake WS dijelaskan di dokumen ini.

| Endpoint | Fungsi |
| --- | --- |
| GET `/api/projects`, POST `/api/projects` | Daftar/buat project; pilih scope HTTP melalui `X-Lab-Project` dan WS melalui hello.project_id |
| GET `/api/jobs/{job_id}` | Status pekerjaan ASYNC; job ID juga tersedia pada response header `X-Lab-Job-ID` |
| POST `/api/agent-lab/ticket` | Ticket browser demo sekali pakai, bukan kredensial backend eksternal |
| GET `/api/agent-lab/status` | Status listener, jumlah koneksi, konfigurasi; bukan handshake |
| POST `/api/agent-lab/listener/start`, `/listener/stop` | Mengaktifkan/menonaktifkan penerimaan telemetry |
| POST `/api/agent-lab/config` | Simpan `{pricing,checks}`; field yang tidak dikenal ditolak |
| GET `/api/agent-lab/sessions` | 200 session terbaru, profile dan connected state; tanpa resume token/hash |
| GET `/api/agent-lab/runs?service_name=...&status=...&limit=100` | Daftar run terbaru, limit 1–500 |
| GET `/api/agent-lab/runs/{trace_id}` | Ringkasan, checks yang dibekukan, verdict dan metadata |
| GET `/api/agent-lab/runs/{trace_id}/events?after=0&limit=500` | Pagination cursor ordinal, items dan has_more; limit 1–1000 |
| GET `/api/agent-lab/runs/{trace_id}/export` | JSON protocol, trace dan seluruh events |
| GET `/api/agent-lab/connection.md` | Dokumen ini, termasuk pada image Docker |
| POST `/api/artifacts`, GET `/api/artifacts/{digest}` | Upload/ambil gambar, dipakai ulang dari fitur dataset |
| GET `/api/datasets`, POST `/api/datasets/import` | Dataset dan reference untuk benchmark berulang |
| GET `/api/scorers` | Daftar scorer yang dipakai engine campaign lama |
| POST `/api/experiments/preflight`, `/api/experiments?launch=true` | Merencanakan/menjalankan perbandingan model melalui provider adapter lab |
| GET `/api/campaigns/{id}/results`, `/report.json`, `/report.md`, `/report.html` | Hasil statistik/report campaign |
| POST `/api/datasets/{id}/predictions` | Import prediksi backend untuk workflow evaluasi offline lama |
| POST `/api/playground` | Prompt + gambar lintas model, SSE; panggilan provider dijalankan lab |

Mengisi LAB_WS_URL saja tidak otomatis menginstrumentasikan service: tambahkan adapter/hook event pada fase penting di backend Anda. Agent lab tidak mengirim perintah eksekusi atau meng-inject test request ke backend, dan belum menggabungkan trace WS menjadi campaign statistik otomatis. Gunakan integration tests Anda untuk memicu service, kirim evaluation, lalu baca verdict/export. Profil router/generic berbagi inspector trace; chart perbandingan router lama tetap memakai campaign dataset engine.

## Arsitektur SYNC / ASYNC dan operasional

Jalankan satu proses SYNC Go (`main.go`) untuk WebSocket, HTTP, session lease, validasi ringan dan commit telemetry. Listener start/stop dan koneksi aktif berada di memori gateway; restart memerlukan Start listening kembali. Session/run/events/config dan artifact berada di PostgreSQL, dipisahkan berdasarkan project. Jalankan ASYNC melalui Lambda SQS ARM64 atau `cmd/worker` lokal. Perhitungan summary/checks, ekspor, impor gambar, panggilan provider, scoring dan analisis dijalankan worker. ACK WS menandakan evidence berhasil commit, bukan bahwa summary ASYNC selesai.

UI meminta summary terbaru melalui HTTP; gateway mengantrekan job dan menunggu hasil maksimal 110 detik sambil tetap melayani koneksi lain. Worker wajib berjalan agar impor, summary/report, campaign dan Playground selesai. Jika HTTP timeout, job tetap durable; periksa `X-Lab-Job-ID` melalui endpoint jobs, lalu ulangi pembacaan. Ini berlaku juga saat frontend berada di Vercel dan koneksi WSS langsung ke EC2. Vercel tidak menjadi server WebSocket.

Rate/checks trace dibekukan saat run dibuat; konfigurasi default dapat diubah untuk run berikutnya. Data disimpan bersama di Neon, sehingga artifact tersedia untuk SYNC dan semua worker tanpa disk lokal bersama. Batas pool default SYNC 4 dan worker 2 koneksi per proses. Concurrency goroutine worker dibatasi, bukan satu goroutine tak terbatas per event/trial.

Untuk campaign yang dijalankan lab sendiri, engine melakukan reservasi budget dan menyimpan attempt sebelum panggilan provider. SQS memiliki semantik at-least-once; lease/deduplikasi melindungi job internal. Jika proses mati saat panggilan berbayar, attempt tidak diasumsikan gratis atau sukses: recovery menandai unknown/interrupted dan membutuhkan reconciliation. Resume eksplisit dapat mengulangi pekerjaan, sehingga periksa catatan provider sebelum melanjutkan. Budget trace eksternal tetap merupakan check evidence; tidak menghentikan pengeluaran service Anda.

[DEPLOYMENT.md](DEPLOYMENT.md) menjelaskan init SQL, konfigurasi Neon, build ZIP/bootstrap ARM64, EC2/nohup, antrean dan Vercel. `sql/init.sql` tidak mentransfer histori SQLite lama; file data lama dibiarkan utuh. Tidak ada retention otomatis untuk trace, staged uploads, jobs, artifact, stream events dan laporan; tetapkan jadwal arsip/cleanup sesuai kebutuhan sebelum pengujian besar.

Redaksi otomatis menyaring key seperti authorization, api_key, password, secret, token dan cookie, serta nilai secret provider/owner yang diketahui lab. Sanitasi data sensitif di backend sebelum mengirim. Kirim keputusan, ringkasan alasan yang aman, input/output/tool evidence yang diperlukan; hindari private reasoning. Seluruh trace adalah evidence yang dikirim service, bukan verifikasi independen bahwa operasi tersebut benar-benar dijalankan.
