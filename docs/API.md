# REST API v1

Semua endpoint `/v1/*`, kecuali register/login, membutuhkan `Authorization: Bearer <token>`. Token bisa berupa JWT dari login atau API key `bm_...`. Error selalu berbentuk `{"error":{"code","message"}}`.

| Method & path | Fitur | Keterangan |
|---|---|---|
| `POST /v1/auth/register` | F5 | Wajib email bisnis; membuat workspace (tenant) + admin |
| `POST /v1/auth/login` | | |
| `GET /v1/me` | | User (`role`, `platform_role`) + workspace (kuota, kredit SMS) |
| `GET /v1/stats/overview?days=14` | | Seri harian terkirim/dibuka/diklik/dibalas (1–90 hari), total + periode sebelumnya, klasifikasi balasan, kualitas kontak |
| `GET/POST /v1/team`, `PATCH /v1/team/{id}` | | Anggota workspace. Tambah/ubah hanya **admin**; `{role: admin\|user, disabled}`; admin aktif terakhir tidak bisa diturunkan |
| `GET /v1/admin/overview?days=30` | | **Super admin**: ringkasan platform, tenant baru per hari, tenant teratas, sebaran paket |
| `GET /v1/admin/tenants?q=&status=`, `PATCH /v1/admin/tenants/{id}` | | **Super admin**: `{status: active\|suspended, plan, daily_send_limit, add_sms_credits}` |
| `GET/POST /v1/api-keys`, `DELETE /v1/api-keys/{id}` | F4/F6 | API key juga dipakai sebagai password SMTP |
| `GET/POST /v1/domains`, `GET /v1/domains/{id}` | F5 | Respons berisi `dns_records` yang perlu dipasang |
| `POST /v1/domains/{id}/check` | F5 | Cek ulang TXT verifikasi, SPF, DKIM, DMARC |
| `POST /v1/finder/domain` | F1 | `{domain, save_contacts, tags}` |
| `POST /v1/finder/person` | F1 | `{first_name, last_name, domain}` |
| `POST /v1/validations` | F2 | Satu email, sinkron |
| `POST /v1/validations/batch` | F2 | ≤100 email, sinkron |
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
