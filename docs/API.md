# REST API v1

Semua endpoint `/v1/*`, kecuali register/login, membutuhkan `Authorization: Bearer <token>`. Token bisa berupa JWT dari login atau API key `bm_...`. Error selalu berbentuk `{"error":{"code","message"}}`.

| Method & path | Fitur | Keterangan |
|---|---|---|
| `POST /v1/auth/register` | F5 | Wajib email bisnis; membuat workspace (tenant) + admin dengan paket `trial` (3 hari, 30 email/hari). Satu domain perusahaan = satu workspace (`409 company_exists`) |
| `POST /v1/auth/login` | | |
| `POST /v1/auth/forgot-password` | | Publik: `{email}`. Selalu `202` (tidak membocorkan apakah akun ada). Tautan reset dikirim dari `SYSTEM_EMAIL_FROM`, berlaku 60 menit, sekali pakai, maks. 3 per jam per akun |
| `POST /v1/auth/reset-password` | | Publik: `{token, password}` (min. 10 karakter). Semua sesi lama berakhir; `422 invalid_reset_token` bila tautan salah/kedaluwarsa |
| `GET /v1/me` | | User (`role`, `platform_role`) + workspace (kuota, kredit SMS) |
| `GET /v1/stats/overview?days=14` | | Seri harian terkirim/dibuka/diklik/dibalas (1–90 hari), total + periode sebelumnya, klasifikasi balasan, kualitas kontak |
| `GET/POST /v1/team`, `PATCH /v1/team/{id}` | | Anggota workspace. Tambah/ubah hanya **admin**; `{role: admin\|user, disabled}`; admin aktif terakhir tidak bisa diturunkan |
| `GET /v1/plans` | | Publik: katalog paket (harga, kuota) dan daftar fitur |
| `GET /v1/billing` | | Paket workspace, tanggal berakhir, sisa hari, pemakaian (email hari ini, kontak, pengguna, domain), permintaan upgrade |
| `POST /v1/billing/upgrade-request` | | **Admin**: `{plan, cycle: monthly\|yearly}`; dicatat untuk diaktifkan Super Admin |
| `GET /v1/admin/overview?days=30` | | **Super admin**: ringkasan platform, tenant baru per hari, tenant teratas, sebaran paket |
| `GET /v1/admin/tenants?q=&status=`, `PATCH /v1/admin/tenants/{id}` | | **Super admin**: `{status: active\|suspended, plan, months, daily_send_limit, add_sms_credits}`. `plan` memulai periode baru (kuota mengikuti paket, `months` bawaan 1); `months` saja memperpanjang |
| `GET/POST /v1/api-keys`, `DELETE /v1/api-keys/{id}` | F4/F6 | API key juga dipakai sebagai password SMTP |
| `GET/POST /v1/domains`, `GET /v1/domains/{id}` | F5 | Respons berisi `dns_records` yang perlu dipasang |
| `POST /v1/domains/{id}/check` | F5 | Cek ulang TXT verifikasi, SPF, DKIM, DMARC |
| `POST /v1/finder/domain` | F1 | `{domain, save_contacts, tags}` |
| `POST /v1/finder/person` | F1 | `{first_name, last_name, domain}` |
| `GET /v1/finder/categories` | F1 | Kategori bisnis dan saran wilayah untuk pencarian per wilayah |
| `POST /v1/finder/searches` | F1 | `{region, category, limit ≤100, crawl_websites, save_contacts, tags}`; berjalan di background. Maks. 20 pencarian/hari per workspace |
| `GET /v1/finder/searches`, `GET/DELETE /v1/finder/searches/{id}` | F1 | Status, ringkasan, dan daftar bisnis (yang punya email lebih dulu) |
| `GET /v1/finder/searches/{id}/export?format=csv\|xlsx` | F1 | Satu baris per email: email, perusahaan, kategori, alamat, kota, telepon, website, sumber |
| `POST /v1/validations` | F2 | Satu email, sinkron |
| `POST /v1/validations/batch` | F2 | ≤100 email, sinkron |
| `POST /v1/validations/lists` | F2 | multipart `file` (.xlsx, .csv, .txt), maks. 20.000 alamat; divalidasi di background |
| `GET /v1/validations/lists`, `GET/DELETE /v1/validations/lists/{id}` | F2 | Progres dan jumlah per status |
| `GET /v1/validations/lists/{id}/export?format=csv\|xlsx\|txt&filter=all\|valid\|deliverable\|invalid` | F2 | Hasil per baris dengan keterangan berbahasa Indonesia; TXT berisi alamat saja |
| `GET/POST /v1/contacts`, `GET/DELETE /v1/contacts/{id}` | F7 | `DELETE ?suppress=true` = permintaan hapus data |
| `POST /v1/contacts/import` | F7 | multipart `file` (.xlsx, .csv, .txt), `tags`. Lihat bagian "Impor kontak" |
| `POST /v1/contacts/validate` | F2 | Validasi massal di background `{tag}` |
| `GET/POST /v1/suppressions`, `DELETE /v1/suppressions/{value}` | §8 | Entri complaint/erasure tidak bisa dihapus |
| `GET/POST /v1/campaigns`, `GET/PUT /v1/campaigns/{id}` | F7 | |
| `POST /v1/campaigns/{id}/test` | F7 | Hanya ke anggota workspace |
| `POST /v1/campaigns/{id}/launch` | F7 | Bisa dipanggil ulang untuk melanjutkan saat kuota habis |
| `GET /v1/campaigns/{id}/stats` | F7 | open/click/reply/bounce rate |
| `GET/POST /v1/sequences`, `GET/PUT /v1/sequences/{id}` | F3 | Lihat contoh definisi di bawah |
| `POST /v1/sequences/{id}/activate` · `/pause` | F3 | |
| `POST /v1/sequences/{id}/enroll` | F3 | `{contact_ids}` atau `{tag}` |
| `GET /v1/sequences/{id}/report` | F3 | Metrik per layer/variant, posisi menunggu, alasan berhenti |
| `GET/POST /v1/aliases`, `DELETE /v1/aliases/{id}` | F8 | |
| `POST /v1/aliases/generate` | F8 | `{domain, pattern, rows}` |
| `GET /v1/inbox?classification=` | F9 | Inbox terpadu |
| `GET/POST /v1/inbound/rules`, `PUT/DELETE /v1/inbound/rules/{id}` | F9/F10 | |
| `POST /v1/otp/send`, `POST /v1/otp/verify` | F4 | |
| `POST /v1/sms`, `GET /v1/sms` | F11 | |
| `GET/POST /v1/webhooks`, `DELETE /v1/webhooks/{id}` | F6 | `{url, events}`; secret ditampilkan sekali |
| `GET /t/o/{token}`, `GET /t/c/{token}` | F7 | Tracking (publik) |
| `GET/POST /u/{token}` | §8 | Unsubscribe 1-klik (publik) |
| `GET /healthz`, `GET /readyz` | | Probe |

## Peran & multi-tenant

Setiap workspace adalah satu **tenant**; semua data (kontak, kampanye, inbox, domain, …) selalu difilter dengan `workspace_id` dari token, sehingga satu tenant tidak bisa melihat data tenant lain.

| Peran | Cakupan | Bisa |
|---|---|---|
| `superadmin` (`platform_role`) | Seluruh platform | `/v1/admin/*`: melihat semua tenant, ubah paket/kuota, tambah kredit SMS, tangguhkan tenant. Diberikan ke email di `SUPERADMIN_EMAILS` saat login; tidak berlaku lewat API key |
| `admin` | Satu tenant | Semua fitur + domain, API key, webhook, tim, alias, aturan inbound, hapus suppression |
| `user` | Satu tenant | Pekerjaan harian: cari/validasi/impor kontak, kampanye, sequence, inbox, OTP, SMS |

Endpoint yang ditolak mengembalikan `403 forbidden`. Peran dan status dibaca ulang dari database di setiap request, sehingga menurunkan peran/menonaktifkan user berlaku seketika. Tenant yang ditangguhkan mendapat `403 workspace_suspended`.

## Validasi daftar dari file

Format file sama dengan impor kontak; contohnya bisa diunduh di `/static/contoh/validasi-contoh.{xlsx,csv,txt}`.

- Excel/CSV: baris pertama judul kolom dengan kolom `email`; `nama depan`, `nama belakang`, dan `perusahaan` ikut diekspor.
- TXT: satu alamat per baris, atau `Nama <email>`. Baris yang diawali `#` diabaikan.
- Alamat yang formatnya salah tetap masuk hasil sebagai `Tidak valid · Format alamat salah`; duplikat dilewati.
- Kolom ekspor: email, nama depan, nama belakang, perusahaan, status, keterangan, skor, server mx, email sementara, alamat peran, email gratis, catch-all, dicek.
- Filter `deliverable` = valid + berisiko (catch-all/alamat peran), cocok untuk daftar kirim.

## Cari prospek per wilayah & kategori

1. Daftar bisnis diambil dari OpenStreetMap lewat Overpass API (`OVERPASS_URL`): bisnis dengan kategori terpilih di dalam batas wilayah administratif (kota, kabupaten, atau provinsi) yang mencantumkan website atau email.
2. Email yang tercantum di listing langsung diambil. Jika `crawl_websites` aktif, website resmi tiap bisnis dibuka dengan crawler yang sama dengan pencarian per domain (patuh robots.txt). Halaman media sosial, marketplace, dan layanan gratis dilewati.
3. Jika `save_contacts` aktif, setiap email disimpan sebagai kontak dengan perusahaan, telepon, dan field `kota`, `kategori`, `alamat`, `website`; batas kontak paket tetap berlaku.

Data bisnis © kontributor OpenStreetMap (ODbL). Server Overpass publik punya kebijakan pemakaian wajar; untuk volume besar jalankan Overpass sendiri dan arahkan `OVERPASS_URL` ke sana.

## Paket & masa coba

| Paket | Harga/bulan | Email/hari | Kontak | Pengguna | Domain |
|---|---|---|---|---|---|
| `trial` (3 hari) | Gratis | 30 | 100 | 1 | 1 |
| `starter` | Rp99.000 | 500 | 2.500 | 2 | 1 |
| `growth` | Rp299.000 | 2.500 | 15.000 | 5 | 3 |
| `business` | Rp799.000 | 10.000 | 75.000 | 15 | 10 |
| `enterprise` | Sesuai kesepakatan | Sesuai kebutuhan | — | — | — |

Harga tahunan = 10× harga bulanan. Semua paket mendapat semua fitur; angka di atas diubah di `internal/billing/plans.go`.

- Melewati batas kontak, pengguna, atau domain mengembalikan `402` dengan kode `plan_limit_contacts`, `plan_limit_users`, atau `plan_limit_domains`.
- Setelah `plan_ends_at` lewat, endpoint kirim (kampanye, sequence, OTP, SMS), finder, dan validasi mengembalikan `402 plan_expired`; email yang masih di antrian gagal dengan alasan yang sama. Data tetap bisa dibaca.
- Belum ada payment gateway (milestone M4): Super Admin mengaktifkan paket secara manual setelah pembayaran dikonfirmasi.

## Impor kontak

| Format | Aturan |
|---|---|
| Excel `.xlsx` | Sheet pertama dibaca. Baris pertama = judul kolom, wajib ada kolom `email`. File `.xls` lama harus disimpan ulang sebagai `.xlsx` |
| CSV / TSV | Pemisah `,` `;` tab atau `\|` dideteksi otomatis, jadi CSV dari Excel berbahasa Indonesia (titik koma) langsung bisa. Encoding UTF-8 (dengan/tanpa BOM) dan UTF-16 didukung |
| TXT | Satu email per baris, atau format `Nama <email>`. Teks bebas yang mengandung email juga bisa. Baris yang diawali `#` diabaikan |

Kolom yang dikenali (Indonesia/Inggris): `email`, `nama depan`, `nama belakang`, `perusahaan`, `jabatan`, `no hp`, `tags` (pisahkan dengan `;`). Kolom lain disimpan sebagai field kustom dan bisa dipakai di template, misalnya `{{kota}}`. Email ganda di dalam satu file dilewati. Respons berisi `format`, jumlah baru/diperbarui/dilewati, dan nomor baris yang bermasalah.

Contoh file bisa diunduh dari dashboard (Kontak → Unduh contoh file) atau langsung:
`/static/contoh/kontak-contoh.xlsx`, `/static/contoh/kontak-contoh.csv`, `/static/contoh/kontak-contoh.txt`.

## Contoh: funnel 3 layer

```json
{
  "name": "Outreach Q4",
  "from_email": "budi@maju.co.id",
  "send_window": {"days": [1,2,3,4,5], "start_hour": 8, "end_hour": 17, "timezone": "Asia/Jakarta"},
  "definition": {
    "start": "e1",
    "settings": {"stop_on_reply": true},
    "nodes": [
      {"id": "e1", "type": "email", "subject": "{Halo|Hai} {{first_name|\"Bapak/Ibu\"}}", "html": "<p>...</p>", "next": "w1",
       "variants": [{"subject": "Kerja sama dengan {{company}}?"}]},
      {"id": "w1", "type": "wait", "wait": {"days": 3}, "next": "c1"},
      {"id": "c1", "type": "condition", "condition": {"type": "opened"}, "yes": "e2", "no": "e3"},
      {"id": "e2", "type": "email", "subject": "Studi kasus", "text": "...", "thread_reply": true, "next": "w2"},
      {"id": "w2", "type": "wait", "wait": {"days": 2}, "next": "a1"},
      {"id": "a1", "type": "action", "action": {"type": "add_tag", "tags": ["hot"]}},
      {"id": "e3", "type": "email", "subject": "Masih relevan?", "text": "...", "next": "end"},
      {"id": "end", "type": "end"}
    ]
  }
}
```

Aturan validasi definisi: ID unik, semua referensi harus ada, tidak boleh ada loop, harus ada node wait di antara dua email, durasi wait antara 1 menit dan 180 hari, jumlah email maksimal sesuai paket (default 20 layer).

## Contoh: OTP

```bash
curl -s -H "Authorization: Bearer $KEY" localhost:8080/v1/otp/send \
  -d '{"to":"user@klien.id","from":"no-reply@maju.co.id","brand":"Maju","fallback_phone":"08123456789","fallback_after_seconds":60}'
```

```bash
curl -s -H "Authorization: Bearer $KEY" localhost:8080/v1/otp/verify -d '{"id":"<id>","code":"123456"}'
```

## Webhook event

Event: `sent`, `open`, `click`, `reply`, `bounce`, `soft_bounce`, `complaint`, `unsubscribe`. Kalau `events` kosong, semua event dikirim.

```json
{"id":"evt_...","type":"bounce","created_at":"2026-11-02T02:00:00Z",
 "data":{"message_id":"…","to":"andi@acme.id","category":"marketing","campaign_id":"…","reason":"5.1.1"}}
```

Setiap request membawa header `X-BalasEmail-Signature: t=<unix>,v1=<hex>`, dengan `v1 = HMAC-SHA256(secret, "<t>.<body>")`. Tolak request yang `t`-nya lebih dari 5 menit dari waktu sekarang (`webhook.Verify` sudah melakukan ini). Respons 2xx berarti sukses. 5xx/429/timeout dicoba ulang hingga 8 kali dengan backoff. 4xx lainnya tidak dicoba ulang. Hanya URL `https://` publik yang diterima; alamat IP privat diblokir.

## Bounce & complaint

Semua email dikirim dengan return-path VERP `b<id><tag>@BOUNCE_DOMAIN`. Pasang record MX `BOUNCE_DOMAIN` ke smtpd. Daftarkan `fbl@BOUNCE_DOMAIN` sebagai alamat feedback loop di Yahoo, Microsoft (JMRP), dan lainnya.

| Laporan | Efek |
|---|---|
| DSN `Action: failed` + status 5.x.x | status `bounced`, suppression `hard_bounce`, sequence berhenti, webhook `bounce` |
| DSN delayed / 4.x.x | event `soft_bounce` saja |
| ARF (selain `not-spam`) | suppression `complaint` (permanen), semua sequence kontak berhenti, webhook `complaint` |

## SMTP

| Port | Fungsi | Auth |
|---|---|---|
| 587 (`SMTP_SUBMISSION_ADDR`) | Relay dari aplikasi pelanggan | `AUTH PLAIN`, username bebas, password = API key |
| 25 (`SMTP_INBOUND_ADDR`) | MX untuk domain pelanggan, plus bridge SMS | SMS butuh header `X-BalasEmail-Key: <api key>` |

Kirim SMS lewat SMTP: kirim email ke `08123456789@sms.balasemail.com`. Isi SMS diambil dari body plain-text (tanda tangan setelah `-- ` dibuang); kalau body kosong, dipakai subjek.
