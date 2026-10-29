# Roadmap & Pengukuran

Dokumen ini menerjemahkan PRD menjadi milestone yang bisa diukur. Setiap item punya **kriteria selesai** yang bisa dicek, baik lewat test otomatis maupun metrik Prometheus.

Status: ✅ selesai di repo · 🟡 sebagian · ⬜ belum

## 1. Status per fitur PRD

| PRD | Sudah ada (bukti) | Belum ada |
|---|---|---|
| F1 Finder | ✅ crawler + robots.txt + obfuskasi + pola nama (`finder_test.go`) | ⬜ pencarian massal ≤1.000 domain sebagai job · ⬜ filter industri/lokasi (butuh sumber data) · ⬜ ekstensi browser |
| F2 Validasi | ✅ 8 tahap + skor (`validator_test.go`), ✅ validasi massal di background | 🟡 probe SMTP butuh IP dengan port 25 terbuka · ⬜ dataset berlabel untuk mengukur akurasi ≥95% · ⬜ job refresh daftar disposable · ⬜ re-validasi otomatis 90 hari terjadwal |
| F3 Funnel | ✅ engine + validasi + window + A/B + auto-stop (`engine_test.go`, `TestSequenceStopsOnReply`) | ⬜ builder visual (frontend) · ⬜ pemilihan pemenang A/B otomatis (laporan per variant sudah ada) |
| F4 OTP | ✅ send/verify/fallback SMS/rate limit (`otp_test.go`, `TestOTPFlow`) | ⬜ SDK JS/PHP/Python |
| F5 Email bisnis | ✅ blokir free/disposable, TXT/SPF/DKIM/DMARC (`service_test.go`) | ⬜ cek DNS berkala + peringatan · ⬜ KYC NIB/NPWP · ⬜ BIMI |
| F6 SMTP Gateway | ✅ submission + MX + DKIM + failover + throttle (`server_test.go`, `mailer_test.go`) | ⬜ parsing bounce asinkron (DSN) & FBL complaint · ⬜ webhook event ke pelanggan · ⬜ MTA sendiri + dedicated IP + warm-up |
| F7 Mass email | ✅ import, segmentasi tag, kuota, laju, tracking, unsubscribe (`TestCampaignFlow`) | ⬜ editor drag-drop · ⬜ spam score checker · ⬜ rotasi mailbox pengirim · ⬜ penjadwalan `scheduled_at` |
| F8 Alias | ✅ pola + bulk + forward (`alias_test.go`) | ⬜ inbox per alias di UI |
| F9 Auto-reply | ✅ aturan + klasifikasi kata kunci + anti-loop (`inbound_test.go`) | ⬜ classifier LLM + draf balasan AI dengan persetujuan manusia |
| F10 Forward | ✅ aturan + round-robin + alignment DMARC | ⬜ tujuan Slack/Telegram/CRM |
| F11 SMS | ✅ bridge SMTP, segmen, Twilio/Zenziva, kredit (`sms_test.go`, `server_test.go`) | ⬜ webhook laporan pengiriman (DLR) · ⬜ top-up kredit |
| §7 Pendukung | ✅ multi-workspace, role owner/admin, metrik | ⬜ dashboard web · ⬜ billing (VA/QRIS/e-wallet) · ⬜ 2FA · ⬜ audit log · ⬜ warm-up mailbox · ⬜ integrasi CRM |
| §8 Kepatuhan | ✅ suppression, footer wajib, hak hapus, auto-suspend reputasi | ⬜ pencatatan persetujuan AUP · ⬜ filter konten terlarang |

## 2. Milestone

Setiap milestone baru dianggap selesai kalau CI hijau, cakupan test tidak turun, dan semua kriteria di kolom terakhir terpenuhi.

| # | Milestone | Isi | Kriteria selesai (terukur) |
|---|---|---|---|
| M0 | **Fondasi** ✅ | Struktur repo, platform, migrasi, CI, 11 modul inti | `make test` + `make cover` ≥55% lulus; ke-3 integration test lulus |
| M1 | **Deliverability produksi** | Parser DSN/FBL di MX → `bounce`/`complaint`; webhook event pelanggan; cek DNS berkala; limiter bersama (Redis) | Bounce asinkron tercatat ≤5 menit; alert `HardBounceRateHigh` dan `SpamComplaintRateHigh` aktif di staging |
| M2 | **Dashboard MVP** | Frontend Next.js: onboarding domain, kontak, kampanye, inbox | Daftar sampai kampanye pertama terkirim < 30 menit (uji 5 pengguna) |
| M3 | **Funnel builder & A/B** | Builder visual, pemenang A/B otomatis (uji signifikansi), template | Sequence ≥10 tahap & ≥2 cabang bisa dibuat dari UI; berhenti ≤5 menit setelah balasan (sudah: sinkron saat inbound) |
| M4 | **Billing & kuota** | Paket, VA/QRIS/e-wallet, top-up kredit SMS, kuota naik bertahap mengikuti reputasi | Upgrade paket langsung menaikkan `daily_send_limit`; kredit SMS bertambah setelah callback pembayaran |
| M5 | **AI inbox** | Classifier LLM di balik `inbound.Classifier`, draf balasan dengan persetujuan | Akurasi klasifikasi ≥90% pada 500 balasan berlabel; dibandingkan dengan baseline kata kunci |
| M6 | **Skala** | MTA sendiri / dedicated IP + warm-up, finder massal, SDK OTP, integrasi CRM | Throughput ≥1 juta email/jam (load test); p95 OTP <10 dtk pada 100 rps |

## 3. KPI → metrik → alert

| KPI PRD | Target | Metrik / kueri | Alert |
|---|---|---|---|
| Hard bounce setelah validasi | < 2% | `balasemail_emails_sent_total{status="bounced"}` ÷ sent+bounced | `HardBounceRateHigh` |
| Spam complaint | < 0,1% | `balasemail_email_events_total{type="complaint"}` (aktif setelah M1) | `SpamComplaintRateHigh` |
| Waktu kirim OTP p95 | < 10 dtk | `histogram_quantile(0.95, balasemail_otp_delivery_seconds_bucket)` | `OTPLatencyP95High` |
| Uptime API/SMTP | ≥ 99,9% | `balasemail_http_requests_total{status=~"5.."}` + probe `/readyz` | `APIHighErrorRate` |
| Latensi API p95 | < 300 ms | `balasemail_http_request_duration_seconds` | `APILatencyP95High` |
| Akurasi validasi | ≥ 95% | Distribusi `balasemail_email_validations_total{status}` + dataset berlabel (M1) | — |
| Konversi funnel | dipantau | `GET /v1/sequences/{id}/report` (reply rate per layer & variant) | — |
| Kesehatan antrian | 0 job mati | `balasemail_jobs_processed_total{outcome="dead"}` | `JobsDeadLettering` |
| Keberhasilan SMS | > 95% | `balasemail_sms_sent_total{status}` | `SMSFailuresHigh` |

Penegakan otomatis yang sudah berjalan: workspace dengan ≥200 kiriman dalam 7 hari, hard bounce >5%, atau complaint >0,3% akan di-*suspend* oleh `delivery.EnforceReputation` (dijalankan tiap 15 menit).

## 4. Definition of Done (per PR)

1. Logika murni punya unit test tabel. Alur yang menyentuh DB punya integration test (`-tags integration`).
2. `make lint test cover` lulus dan cakupan tidak turun dari baseline (saat ini 59%).
3. Endpoint baru tercatat di `docs/API.md`. Perubahan skema dibuat sebagai file migrasi baru, bukan mengedit migrasi lama.
4. Fitur yang mengirim pesan memakai `delivery.Queue` / `sms.Service.Send`, tidak boleh jalur sendiri.
5. Fitur yang punya KPI menambah metrik di `internal/platform/metrics` dan, kalau relevan, alert di `deploy/alerts.yml`.
