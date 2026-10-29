<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="internal/web/static/brand/logo-horizontal-dark.svg">
    <img src="internal/web/static/brand/logo-horizontal.svg" alt="BalasEmail" width="360">
  </picture>
</p>

# BalasEmail.com

Platform email outreach & otomasi komunikasi B2B (Golang). Spesifikasi produk ada di [`docs/PRD.md`](docs/PRD.md); rencana kerja terukur ada di [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Isi repo

| Fitur PRD | Modul | Ringkasan |
|---|---|---|
| F1 Email finder | `internal/finder` | Crawler sopan (patuh robots.txt, rate limit, hanya domain perusahaan), ekstraksi email terobfuskasi, tebak pola nama dan verifikasi via SMTP |
| F2 Validasi | `internal/validator` | Sintaks, MX/null-MX, disposable, role-based, free provider, probe RCPT, deteksi catch-all, skor 0–100 |
| F3 Funnel multi-layer | `internal/sequence` | Graf DAG berisi node email/wait/condition/action, jumlah layer bisa diatur, A/B variant, jam kirim per zona waktu, auto-stop saat ada balasan/bounce/unsubscribe |
| F4 OTP | `internal/otp` | Kode di-hash (HMAC, terikat ID), TTL, batas percobaan, rate limit, antrian prioritas, fallback ke SMS |
| F5 Wajib email bisnis | `internal/auth`, `internal/senderdomain` | Tolak Gmail/Yahoo/disposable; verifikasi domain via TXT; cek SPF/DKIM/DMARC; kunci DKIM 2048-bit terenkripsi |
| F6 SMTP Gateway | `internal/smtpgw`, `internal/mailer`, `internal/delivery` | Submission relay (AUTH memakai API key), MX inbound, DKIM signing, failover provider + circuit breaker, throttle per domain, bounce menjadi suppression |
| F7 Mass email | `internal/campaign`, `internal/contact` | Import Excel (.xlsx), CSV, dan TXT dengan contoh file yang bisa diunduh, segmentasi tag, spintax + personalisasi, kuota harian, laju kirim, tracking open/klik, unsubscribe 1-klik (RFC 8058) |
| F8 Alias unik | `internal/alias` | Generator pola `{first}.{division}`, bulk, auto-suffix saat bentrok, forward per alias |
| F9 Auto-reply | `internal/inbound` | Rule engine, klasifikasi balasan (ID/EN), anti-loop (RFC 3834) + batas 1x/24 jam per pengirim |
| F10 Auto-forward | `internal/inbound` | Forward berbasis aturan/klasifikasi, round-robin antar sales, dikirim ulang dari domain sendiri agar lolos DMARC |
| F11 SMS via SMTP | `internal/sms`, `internal/smtpgw` | `08xx@sms.<domain>` → SMS; normalisasi nomor Indonesia; hitung segmen GSM-7/UCS-2; Twilio/Zenziva + failover; sistem kredit |
| M1 Deliverability | `internal/bounce`, `internal/webhook`, `internal/platform/ratelimit`, `internal/platform/netguard` | Return-path VERP, parser DSN (RFC 3464) & ARF (RFC 5965), webhook event bertanda tangan, limiter bersama lintas worker, klien HTTP anti-SSRF, cek DNS berkala |
| Multi-tenant & peran | `internal/auth`, `internal/app/policy.go`, `internal/admin` | Tiap workspace = tenant terisolasi. Peran **Super Admin** (konsol platform: semua tenant, paket, kuota, kredit SMS, tangguhkan; dari `SUPERADMIN_EMAILS`), **Admin** (kelola domain, tim, API key, webhook, aturan), **User** (operasional harian). Kebijakan akses terpusat per rute |
| Dashboard | `internal/web`, `internal/stats` | SPA tanpa dependensi yang disajikan oleh `api` di `/`, warna mengikuti palet brand (terang & gelap): grafik aktivitas harian 7/14/30 hari, sparkline, klasifikasi balasan, kualitas kontak, konsol platform; domain, kontak, finder, validasi, kampanye, sequence, inbox & aturan, alias, OTP, SMS, API key, webhook, daftar blokir |
| §8 Kepatuhan | `internal/suppression`, `internal/delivery` | Suppression global/workspace, footer identitas pengirim wajib, hak hapus data, auto-suspend jika bounce >5% atau complaint >0,3% |

## Arsitektur singkat

```
cmd/api     → REST API + tracking + halaman unsubscribe         (:8080, metrics :9090)
cmd/worker  → job queue (kirim email/SMS, validasi, webhook) +
              scheduler (sequence tick 30 dtk, reputasi 15 mnt)  (metrics :9091)
cmd/smtpd   → SMTP submission (587) + MX inbound (25)           (metrics :9092)
                       │
                PostgreSQL (data + antrian job FOR UPDATE SKIP LOCKED)
```

Detailnya ada di [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md), daftar endpoint di [`docs/API.md`](docs/API.md).

```
cmd/                 entrypoint per proses
internal/app/        wiring dependency + integration test
internal/platform/   apperr, authctx, database, httpx, logger, metrics, queue, security
internal/<fitur>/    satu paket per kapabilitas: service.go (logika), http.go (handler), *_test.go
migrations/          skema SQL ter-embed, otomatis diterapkan saat start
deploy/              docker-compose, Prometheus, alert KPI
docs/                PRD, roadmap, arsitektur, API
```

## Menjalankan

Prasyarat: Go 1.26+, Docker.

```bash
cp .env.example .env
```

```bash
make up        # postgres, api, worker, smtpd, mailpit (UI email :8025), prometheus (:9095)
```

Atau jalankan proses secara lokal:

```bash
make db
```

```bash
make run-api    # terminal 1
```

```bash
make run-worker # terminal 2
```

```bash
make run-smtpd  # terminal 3
```

Buka **http://localhost:8080** untuk dashboard web: daftar dengan email perusahaan, tambahkan domain, impor kontak, lalu buat kampanye atau sequence. Dengan `DEV_SKIP_DNS_CHECK=true` (bawaan `.env.example` dan docker-compose), domain langsung dianggap terverifikasi, jadi email bisa terkirim ke Mailpit (**http://localhost:8025**) tanpa memasang DNS. Pengaturan ini diabaikan saat `APP_ENV=production`.

Contoh lewat API:

```bash
curl -s localhost:8080/v1/auth/register -d '{"company_name":"PT Maju","full_name":"Budi","email":"budi@maju.co.id","password":"password-kuat-123","postal_address":"Jakarta"}'
```

## Kualitas & pengukuran

| Perintah | Isi |
|---|---|
| `make test` | Unit test + race detector (tanpa database) |
| `make test-integration` | Alur end-to-end terhadap PostgreSQL: kampanye → tracking → unsubscribe, OTP, sequence berhenti saat ada balasan |
| `make cover` | Cakupan gabungan; gagal jika di bawah `COVER_MIN` (55%) |
| `make lint` | `go vet` + golangci-lint |

CI (`.github/workflows/ci.yml`) menjalankan lint, test, integration test dengan gate cakupan, dan build image. KPI PRD dipetakan ke metrik Prometheus dan alert di `deploy/alerts.yml` (lihat ROADMAP §KPI).

## Logo

File logo ada di `internal/web/static/brand/`:

| File | Kegunaan |
|---|---|
| `logo-mark.svg`, `logo-512.png`, `logo-192.png` | Ikon aplikasi (gelembung chat + amplop, titik hijau = ada balasan baru) |
| `logo-horizontal.svg/.png`, `logo-horizontal-dark.svg/.png` | Logo dengan tulisan, untuk latar terang dan gelap |
| `favicon.svg`, `logo-32.png`, `apple-touch-icon.png` | Ikon browser dan home screen |

Warna utama: biru `#2563EB` → indigo `#4F46E5` → ungu `#7C3AED`, aksen hijau `#34D399`.

