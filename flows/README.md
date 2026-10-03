# Flow Langflow Operan

Lima flow AI Operan dipanggil backend lewat `POST {LANGFLOW_URL}/api/v1/run/{FLOW_ID}`.
Selama flow belum dibuat, backend memakai mock (`LANGFLOW_MOCK=true`) sehingga aplikasi tetap bisa dicoba.

| # | Nama | Env | Prompt | Skema output |
|---|---|---|---|---|
| 1 | extract_voice_log | `FLOW_ID_EXTRACT_VOICE` | `prompts/01-extract-voice.md` | `schemas/extract_voice_log.schema.json` |
| 2 | handover_summary | `FLOW_ID_HANDOVER` | `prompts/02-handover.md` | `schemas/handover_summary.schema.json` |
| 3 | trend_analysis | `FLOW_ID_TREND` | `prompts/03-trend.md` | `schemas/trend_analysis.schema.json` |
| 4 | doctor_report | `FLOW_ID_DOCTOR_REPORT` | `prompts/04-doctor-report.md` | `schemas/doctor_report.schema.json` |
| 5 | ask_history (Agent) | `FLOW_ID_ASK_HISTORY` | `prompts/05-ask-history.md` | `schemas/ask_history.schema.json` |

Semua flow menerima payload sebagai **pesan chat berisi string JSON** (`input_type: chat`) dan membalas lewat
Chat Output. Backend mengambil teks dari `outputs[0].outputs[0].results.message.text`, membuang code fence
```json, lalu memvalidasi bentuknya. Jika gagal (timeout 30 detik, retry 1x), aplikasi menampilkan fallback.

## 1. Menjalankan Langflow lokal

Langflow butuh Python 3.10–3.13. Di komputer pengembang dipasang di venv terpisah:

```powershell
uv venv C:\Users\JE\langflow-env --python 3.12
$env:VIRTUAL_ENV="C:\Users\JE\langflow-env"; uv pip install langflow
C:\Users\JE\langflow-env\Scripts\langflow.exe run --host 127.0.0.1 --port 7860
```

**Wajib untuk Flow 5:** jalankan Langflow dengan `LANGFLOW_SSRF_ALLOWED_HOSTS=localhost,127.0.0.1`.
Tanpa ini, proteksi SSRF Langflow menolak tool API Request yang memanggil backend di localhost.

```powershell
$env:LANGFLOW_SSRF_ALLOWED_HOSTS="localhost,127.0.0.1"; langflow run --host 127.0.0.1 --port 7860
```

Backend memberi agent alamat tool dari `PUBLIC_BASE_URL`. Isi dengan IP (`http://127.0.0.1:8080`), bukan
`localhost`: validasi URL di komponen API Request menolak host tanpa TLD ("Invalid URL provided").

Cara paling cepat membuat ulang kelima flow lewat API: `python flows/build_flows.py`
(prompt diambil dari `prompts/`, model Gemini dari variabel global `GOOGLE_API_KEY`).

Langflow cukup berat (±2–3 GB RAM). Tutup aplikasi lain bila komputer melambat.

## 2. Model: Google AI Studio (Gemini)

1. Buat API key di https://aistudio.google.com/apikey.
2. Di Langflow: **Settings → Global Variables → Add**, nama `GOOGLE_API_KEY`, tipe Credential.
3. Di setiap flow pakai komponen **Google Generative AI** (atau **Language Model** dengan provider Google),
   model `gemini-3.1-flash-lite-preview` (cepat, kuota gratis lebih longgar; bisa diganti lewat `GEMINI_MODEL`), temperature `0.1`, API key dari variabel `GOOGLE_API_KEY`.

## 3. Membuat flow 1–4 (pola sama)

```
Chat Input ──► Prompt ──► Google Generative AI ──► Chat Output
```

1. **New Flow → Blank Flow**, beri nama sesuai tabel (misalnya `extract_voice_log`).
2. Tambah **Chat Input**.
3. Tambah **Prompt**. Salin isi blok "System prompt" dari file `prompts/0X-*.md` ke Template.
   Template berisi variabel `{payload}`; hubungkan output **Chat Input → Message** ke input `payload`.
4. Tambah **Google Generative AI**. Hubungkan **Prompt → Prompt Message** ke input model.
5. Tambah **Chat Output**, hubungkan output model ke sana.
6. Uji di **Playground** dengan contoh input dari file prompt. Pastikan balasannya hanya JSON.

## 4. Membuat flow 5 (Agent + tools)

```
Chat Input ──► Agent ──► Chat Output
                 ▲
     API Request (tool) × 1   (agent mengisi URL sendiri)
```

1. Tambah **Agent**. Model provider Google, model `gemini-3.1-flash-lite-preview` (cepat, kuota gratis lebih longgar; bisa diganti lewat `GEMINI_MODEL`).
   Isi **Agent Instructions** dengan blok dari `prompts/05-ask-history.md`.
2. Tambah **API Request**, aktifkan **Tool Mode**. Method `GET`. Header:
   `X-Internal-Token` = nilai `INTERNAL_TOOL_TOKEN` dari `.env` backend.
   Biarkan URL diisi agent (instruksi sudah menjelaskan tiga URL yang tersedia).
3. Hubungkan **API Request → Toolset** ke input **Tools** Agent, Chat Input → Agent, Agent → Chat Output.
4. Pastikan Langflow bisa menjangkau backend di `PUBLIC_BASE_URL` (default `http://localhost:8080`).
   Uji paling mudah lewat aplikasi (tab **Tanya**), karena backend yang membuat `tool_token` sesi.

Endpoint tools yang dipanggil agent (read-only, dibatasi satu profil per sesi 10 menit):

```
GET /internal/tools/profiles/:id/logs?from=&to=&type=&session=
GET /internal/tools/profiles/:id/medications?session=
GET /internal/tools/profiles/:id/doses?from=&to=&session=
```

## 5. Menyambungkan ke backend

1. Di Langflow: **Settings → Langflow API Keys → Create**. Salin ke `LANGFLOW_API_KEY` di `.env`.
2. Buka tiap flow → **API** (pojok kanan atas) → salin ID flow dari URL `.../api/v1/run/<FLOW_ID>`
   ke `FLOW_ID_*` yang sesuai.
3. Set `LANGFLOW_MOCK=false`, jalankan ulang backend.
4. Ekspor tiap flow (**⋯ → Export**) ke folder ini sebagai `extract_voice_log.json`, `handover_summary.json`,
   `trend_analysis.json`, `doctor_report.json`, `ask_history.json`. Hapus API key dari file sebelum di-commit.

Untuk memasang kelima flow di Langflow lain (lokal atau server): `python flows/build_flows.py`.
Script ini membaca prompt di `prompts/`, membuat/memperbarui flow di project **Operan**, mencetak baris
`FLOW_ID_*` untuk `.env`, dan menulis ulang file export di folder ini.

## 6. Speech-to-text

Backend mentranskrip audio (WAV 16 kHz dari aplikasi) dengan Gemini sebelum memanggil flow 1.
Isi `STT_API_KEY` dengan API key Google AI Studio yang sama, `STT_PROVIDER=gemini`, `STT_MOCK=false`.

## 7. MCP untuk IBM Bob

Di Langflow: **Project → MCP Server** → aktifkan flow yang ingin diekspos. Salin konfigurasi MCP
(SSE URL) ke IBM Bob, lalu minta Bob memanggil tiap flow dengan contoh dari `prompts/` dan
membandingkan hasilnya dengan `schemas/`.
