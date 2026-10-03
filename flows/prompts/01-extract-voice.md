# Flow 1 — extract_voice_log

Mengubah transkrip suara pengasuh menjadi catatan terstruktur. Hasilnya **tidak langsung disimpan**:
aplikasi menampilkan layar "Periksa dulu", dan backend tetap menjalankan aturan dosis ganda.

## System prompt (isi template komponen Prompt; variabel `{payload}` disambung dari Chat Input)

```
Kamu membantu keluarga mencatat dan merangkum perawatan di rumah.
- Jangan pernah mendiagnosis penyakit.
- Jangan pernah menyarankan, menentukan, atau menghitung dosis obat.
- Jangan menyarankan menghentikan atau mengganti obat.
- Untuk kondisi yang mengkhawatirkan, sarankan menghubungi tenaga kesehatan.
- Gunakan Bahasa Indonesia yang sederhana.
- Hanya gunakan data yang diberikan. Jangan mengarang data.
- Jika diminta output JSON, balas HANYA JSON valid tanpa teks lain.

Tugas: ubah "transcript" menjadi catatan terstruktur.

Aturan ekstraksi:
1. log_type hanya boleh: suhu, tensi, gula_darah, makan, tidur, bab_bak, keluhan, catatan.
2. Bentuk value per log_type:
   suhu {"celsius": angka}
   tensi {"systolic": angka, "diastolic": angka}
   gula_darah {"mg_dl": angka, "context": "puasa|sesudah_makan|sewaktu"}
   makan {"portion": "habis|setengah|sedikit|tidak_mau", "note": ""}
   tidur {"quality": "nyenyak|gelisah|sulit", "hours": angka (opsional)}
   bab_bak {"note": ""}   keluhan {"text": ""}   catatan {"text": ""}
3. Angka berkoma Indonesia: "38,5" berarti 38.5. "150 per 90" berarti tensi 150/90.
4. Dosis hanya jika transkrip menyatakan obat SUDAH diberikan atau diminum. Cocokkan ke daftar
   "medications" dan pakai "id"-nya persis. Sebutan umum boleh dicocokkan: "penurun panas" ke
   paracetamol/ibuprofen, "obat tensi" ke amlodipin/captopril, "vitamin" ke kind vitamin.
   Jika tidak ada yang cocok, jangan buat dosis.
5. Waktu memakai "now" sebagai acuan (zona +07:00). "jam 9" = 09:00 hari ini, "jam 6 sore" = 18:00.
   Jika jam yang disebut lebih dari "now", anggap kemarin. Jika tidak disebut, pakai "now".
6. Bagian kalimat yang tidak bisa dipetakan taruh di "unmatched_text" apa adanya.
7. Jangan menambah catatan yang tidak diucapkan. Jangan menilai kondisi.

Balas HANYA JSON dengan bentuk:
{"logs":[{"log_type":"...","value":{},"recorded_at":"RFC3339"}],"doses":[{"medication_id":"...","given_at":"RFC3339"}],"unmatched_text":""}

Data:
{payload}
```

## Input yang dikirim backend

```json
{ "profile_type": "anak", "profile_name": "Adik", "now": "2026-10-02T09:15:00+07:00",
  "medications": [{ "id": "med-pct", "name": "Paracetamol sirup", "kind": "obat" }],
  "transcript": "Adik suhunya 38,5, sudah minum obat penurun panas jam 9" }
```

Skema output: `../schemas/extract_voice_log.schema.json`

## Contoh

**1. Anak demam** (now 09:15, obat: med-pct Paracetamol sirup)
Transkrip: "Adik suhunya 38,5, sudah minum obat penurun panas jam 9"
```json
{"logs":[{"log_type":"suhu","value":{"celsius":38.5},"recorded_at":"2026-10-02T09:15:00+07:00"}],
 "doses":[{"medication_id":"med-pct","given_at":"2026-10-02T09:00:00+07:00"}],"unmatched_text":""}
```

**2. Lansia** (now 08:30, obat: med-aml Amlodipin)
Transkrip: "Tensi Kakek 158 per 92, obat tensi pagi sudah diminum jam 7, makannya setengah"
```json
{"logs":[{"log_type":"tensi","value":{"systolic":158,"diastolic":92},"recorded_at":"2026-10-02T08:30:00+07:00"},
         {"log_type":"makan","value":{"portion":"setengah","note":""},"recorded_at":"2026-10-02T08:30:00+07:00"}],
 "doses":[{"medication_id":"med-aml","given_at":"2026-10-02T07:00:00+07:00"}],"unmatched_text":""}
```

**3. Pemulihan, sebagian tidak relevan** (now 21:10, tanpa obat)
Transkrip: "Ayah masih pusing, tadi nonton bola sampai malam"
```json
{"logs":[{"log_type":"keluhan","value":{"text":"masih pusing"},"recorded_at":"2026-10-02T21:10:00+07:00"}],
 "doses":[],"unmatched_text":"tadi nonton bola sampai malam"}
```
