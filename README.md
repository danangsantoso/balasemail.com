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
| F7 Mass email | `internal/campaign`, `internal/contact` | Import CSV (header ID/EN), segmentasi tag, spintax + personalisasi, kuota harian, laju kirim, tracking open/klik, unsubscribe 1-klik (RFC 8058) |
| F8 Alias unik | `internal/alias` | Generator pola `{first}.{division}`, bulk, auto-suffix saat bentrok, forward per alias |
| F9 Auto-reply | `internal/inbound` | Rule engine, klasifikasi balasan (ID/EN), anti-loop (RFC 3834) + batas 1x/24 jam per pengirim |
| F10 Auto-forward | `internal/inbound` | Forward berbasis aturan/klasifikasi, round-robin antar sales, dikirim ulang dari domain sendiri agar lolos DMARC |
| F11 SMS via SMTP | `internal/sms`, `internal/smtpgw` | `08xx@sms.<domain>` → SMS; normalisasi nomor Indonesia; hitung segmen GSM-7/UCS-2; Twilio/Zenziva + failover; sistem kredit |
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

Contoh cepat:

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
