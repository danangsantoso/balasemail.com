# REST API v1

Semua endpoint `/v1/*`, kecuali register/login, membutuhkan `Authorization: Bearer <token>`. Token bisa berupa JWT dari login atau API key `bm_...`. Error selalu berbentuk `{"error":{"code","message"}}`.

| Method & path | Fitur | Keterangan |
|---|---|---|
| `POST /v1/auth/register` | F5 | Wajib email bisnis; membuat workspace + owner |
| `POST /v1/auth/login` | | |
| `GET /v1/me` | | User + workspace (kuota, kredit SMS) |
| `GET/POST /v1/api-keys`, `DELETE /v1/api-keys/{id}` | F4/F6 | API key juga dipakai sebagai password SMTP |
| `GET/POST /v1/domains`, `GET /v1/domains/{id}` | F5 | Respons berisi `dns_records` yang perlu dipasang |
| `POST /v1/domains/{id}/check` | F5 | Cek ulang TXT verifikasi, SPF, DKIM, DMARC |
| `POST /v1/finder/domain` | F1 | `{domain, save_contacts, tags}` |
| `POST /v1/finder/person` | F1 | `{first_name, last_name, domain}` |
| `POST /v1/validations` | F2 | Satu email, sinkron |
| `POST /v1/validations/batch` | F2 | ≤100 email, sinkron |
| `GET/POST /v1/contacts`, `GET/DELETE /v1/contacts/{id}` | F7 | `DELETE ?suppress=true` = permintaan hapus data |
| `POST /v1/contacts/import` | F7 | multipart `file` (CSV), `tags` |
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
| `GET /t/o/{token}`, `GET /t/c/{token}` | F7 | Tracking (publik) |
| `GET/POST /u/{token}` | §8 | Unsubscribe 1-klik (publik) |
| `GET /healthz`, `GET /readyz` | | Probe |

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

## SMTP

| Port | Fungsi | Auth |
|---|---|---|
| 587 (`SMTP_SUBMISSION_ADDR`) | Relay dari aplikasi pelanggan | `AUTH PLAIN`, username bebas, password = API key |
| 25 (`SMTP_INBOUND_ADDR`) | MX untuk domain pelanggan, plus bridge SMS | SMS butuh header `X-BalasEmail-Key: <api key>` |

Kirim SMS lewat SMTP: kirim email ke `08123456789@sms.balasemail.com`. Isi SMS diambil dari body plain-text (tanda tangan setelah `-- ` dibuang); kalau body kosong, dipakai subjek.
