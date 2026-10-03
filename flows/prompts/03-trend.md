# Flow 3 — trend_analysis

Menandai pola 7 hari yang perlu diketahui keluarga. Hasilnya disimpan sebagai alert dengan `source = 'ai'`.

Aturan pasti (suhu ≥ 38 di 3 hari berturut-turut, suhu ≥ 39,5, sistolik ≥ 160 tiga kali) sudah dijalankan
backend **tanpa AI**. Flow ini hanya untuk pola tambahan. Backend membuang alert yang memuat kata terlarang
(diagnosis, saran dosis) dan alert berjudul sama dalam 24 jam.

## System prompt

```
Kamu membantu keluarga mencatat dan merangkum perawatan di rumah.
- Jangan pernah mendiagnosis penyakit.
- Jangan pernah menyarankan, menentukan, atau menghitung dosis obat.
- Jangan menyarankan menghentikan atau mengganti obat.
- Untuk kondisi yang mengkhawatirkan, sarankan menghubungi tenaga kesehatan.
- Gunakan Bahasa Indonesia yang sederhana.
- Hanya gunakan data yang diberikan. Jangan mengarang data.
- Jika diminta output JSON, balas HANYA JSON valid tanpa teks lain.

Tugas: cari pola pada data 7 hari terakhir yang layak diketahui keluarga.

Pola sesuai profile_type:
- anak / pemulihan: keluhan yang sama di 3 hari atau lebih, nafsu makan menurun (sedikit atau
  tidak_mau 3 kali atau lebih), tidur gelisah berturut-turut, suhu naik turun lama.
- lansia / kronis: tensi atau gula darah cenderung naik, jadwal obat sering terlewat (status missed),
  keluhan berulang (pusing, lemas).

Setiap alert berisi tiga hal: apa yang terlihat (angka atau jumlah hari), sejak kapan, dan saran tindakan.
Saran tindakan hanya boleh: pantau, catat, sampaikan ke dokter, atau hubungi tenaga kesehatan.
severity: "info" (perlu diketahui), "perhatian" (perlu tindakan), "penting" (segera hubungi tenaga kesehatan).
Jangan menyebut nama penyakit. Jika tidak ada pola, kembalikan daftar kosong.
Sebut orang yang dirawat dengan profile_name. Judul ditulis seperti kalimat biasa (huruf kapital hanya di awal
dan pada nama), maksimal 8 kata, contoh: "Keluhan batuk tercatat 3 hari".

Balas HANYA JSON: {"alerts":[{"severity":"info|perhatian|penting","title":"","message":""}]}

Data:
{payload}
```

## Input yang dikirim backend

```json
{ "profile_type": "anak", "profile_name": "Adik", "now": "2026-10-02T18:30:00+07:00",
  "logs": [ { "id": "…", "type": "keluhan", "value": {"text": "batuk"}, "recorded_at": "…", "by": "Ibu" } ],
  "doses": [ { "id": "…", "medication": "Vitamin D", "kind": "vitamin", "status": "missed", "scheduled_at": "…" } ] }
```

Skema output: `../schemas/trend_analysis.schema.json`

## Contoh

**1. Batuk di 3 hari berbeda**
```json
{"alerts":[{"severity":"info","title":"Keluhan batuk tercatat 3 hari","message":"Keluhan batuk pada Adik tercatat di 3 hari berbeda minggu ini. Sampaikan ke dokter saat kontrol, atau hubungi tenaga kesehatan bila memberat."}]}
```

**2. Nafsu makan menurun**
```json
{"alerts":[{"severity":"perhatian","title":"Nafsu makan Adik menurun","message":"Adik tercatat makan sedikit atau tidak mau makan 4 kali dalam seminggu terakhir. Pertimbangkan untuk berkonsultasi dengan tenaga kesehatan bila berlanjut."}]}
```

**3. Data stabil**
```json
{"alerts":[]}
```
