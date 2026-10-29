# Arsitektur

## Prinsip

1. **Modular monolith.** Satu repo, satu modul Go, tiga proses (`api`, `worker`, `smtpd`). Setiap fitur PRD adalah paket di `internal/` dengan batas yang jelas, jadi nanti bisa dipecah menjadi service terpisah tanpa menulis ulang.
2. **Satu pipeline pengiriman.** Semua email keluar (kampanye, sequence, OTP, auto-reply, forward, relay SMTP) lewat `delivery.Queue`. Karena itu suppression, footer unsubscribe, tracking, DKIM, throttle, failover, dan bounce handling cukup diterapkan di satu tempat.
3. **Logika murni dipisah dari I/O.** Mesin funnel (`sequence.Advance`), validasi (`validator`), klasifikasi, rule matching, verifikasi OTP (`otp.check`), dan pola alias tidak menyentuh database, sehingga bisa diuji tabel per tabel.
4. **Satu dependency stateful.** PostgreSQL menyimpan data sekaligus antrian job (`FOR UPDATE SKIP LOCKED`). Redis/Kafka bisa masuk nanti di balik kontrak `queue.Queue`/`queue.Handler`.
5. **Terukur sejak awal.** Setiap KPI PRD punya metrik Prometheus (`internal/platform/metrics`) dan aturan alert (`deploy/alerts.yml`).

## Alur utama

### Kampanye (F7)
```
POST /v1/campaigns/{id}/launch
  → AuthorizeSender (domain terverifikasi + SPF + DKIM)        F5
  → RemainingQuota (kuota harian workspace)                    §8
  → pilih kontak: tag cocok, belum unsubscribe, bukan invalid
  → per kontak: spintax + variabel → delivery.Queue (run_at berjeda sesuai send_per_minute)
worker: email.send
  → cek suppression ulang → build MIME + List-Unsubscribe → DKIM sign
  → throttle per domain penerima → Router (primary → fallback)
  → sent | bounced (→ suppression hard_bounce) | retry dengan backoff
```

### Funnel follow-up (F3)
```
worker tiap 30 dtk: Sequences.Tick
  → enrollment jatuh tempo (SKIP LOCKED)
  → facts = engagement email terakhir (open/click/reply) + tag/field kontak
  → Advance(def, node, facts, now, sendWindow) → efek + node berikutnya + run_at
  → efek: kirim email (A/B deterministik), tag, SMS, webhook, notifikasi
event delivery (reply/bounce/unsubscribe/click) → enrollment dihentikan otomatis
```

### Inbound (F8–F10)
```
smtpd MX: RCPT hanya untuk domain pelanggan terverifikasi (bukan open relay)
  → Inbound.Receive: parse MIME → IsAutomated (RFC 3834) → klasifikasi
  → In-Reply-To/References dicocokkan ke messages.message_id_header → RecordReply
  → forward per alias → aturan berurutan prioritas: auto_reply (maks 1x/24 jam) | forward (round-robin)
```

### SMS via SMTP (F11)
```
Submission (AUTH API key)  : RCPT 08xx@sms.<domain> → sms.FromEmail
MX + header X-BalasEmail-Key: RCPT 08xx@sms.<domain> → sms.FromEmail
  → normalisasi E.164 → hitung segmen → potong kredit (atomik) → job sms.send
  → Router Twilio → Zenziva (failover); jika gagal permanen, kredit dikembalikan
```

## Keamanan

- Password memakai bcrypt. Sesi memakai JWT HS256 dengan issuer tetap. API key `bm_<prefix>_<secret>` hanya disimpan hash SHA-256-nya dan dibandingkan secara constant-time.
- Kunci privat DKIM dan kode OTP untuk fallback dienkripsi AES-256-GCM. Hash OTP berupa HMAC yang terikat ID OTP.
- Token tracking/unsubscribe ditandatangani HMAC dengan prefix tujuan (`o:`, `c:`, `u:`) supaya token klik tidak bisa dipakai sebagai token unsubscribe. URL klik ikut ditandatangani, jadi tidak ada open redirect.
- Unsubscribe lewat GET hanya menampilkan konfirmasi, karena link scanner sering mem-prefetch URL. Unsubscribe benar-benar terjadi lewat POST (RFC 8058).
- SMTP: header `From` harus satu domain dengan `MAIL FROM` yang sudah diotorisasi, header `Bcc` dibuang, dan MX menolak penerima di luar domain pelanggan.
- Rate limit per IP untuk endpoint publik auth. Rate limit per penerima untuk OTP.

## Batasan saat ini (lihat ROADMAP)

- Throttle per domain dan rate limit IP masih per proses. Untuk banyak instance perlu limiter bersama (Redis).
- Bounce asinkron (DSN yang datang belakangan) dan feedback loop complaint belum diparse. Yang sudah ditangani baru bounce sinkron dari relay.
- Daftar disposable/free provider masih berupa seed kecil dan perlu job pembaruan berkala.
