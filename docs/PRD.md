# PRD — BalasEmail.com

**Platform Email Outreach & Otomasi Komunikasi B2B**

| Item | Keterangan |
|---|---|
| Versi dokumen | 1.0 (Draft) |
| Tanggal | 29 Oktober 2026 |
| Pemilik produk | Danang Santoso |
| Status | Untuk direview |

---

## 1. Ringkasan

BalasEmail.com adalah platform SaaS untuk tim sales, marketing, dan operasional B2B yang menggabungkan seluruh siklus komunikasi email dalam satu tempat:

1. **Menemukan prospek** — mencari email bisnis perusahaan dari sumber publik.
2. **Membersihkan data** — memvalidasi email aktif agar reputasi pengirim terjaga.
3. **Menjangkau** — mengirim email massal dan sequence follow-up bertingkat (multi-layer funnel).
4. **Merespons otomatis** — auto-reply, auto-forward, dan routing balasan.
5. **Infrastruktur** — SMTP Gateway, layanan OTP via email bisnis, dan jembatan Email-to-SMS.

Semua aktivitas pengiriman **wajib menggunakan email/domain bisnis terverifikasi** (bukan Gmail/Yahoo/Outlook gratis) untuk menjaga deliverability dan kredibilitas.

## 2. Latar Belakang & Masalah

| Masalah | Dampak |
|---|---|
| Tim sales memakai banyak tool terpisah (finder, verifier, sender, CRM) | Biaya tinggi, data tersebar, sulit dilacak |
| Daftar email tidak tervalidasi | Bounce rate tinggi → domain masuk blacklist |
| Follow-up dilakukan manual | Prospek terlewat, konversi rendah |
| Email dikirim dari akun gratis | Dianggap spam, tidak profesional |
| OTP/notifikasi transaksional butuh layanan terpisah | Integrasi rumit & mahal |
| Pelanggan di Indonesia masih sangat bergantung pada SMS | Perlu kanal SMS tanpa integrasi baru |

## 3. Tujuan & Metrik Keberhasilan

### Tujuan Bisnis
- Menjadi platform outreach B2B all-in-one dengan fokus pasar Indonesia & Asia Tenggara.
- Mencapai 500 akun berbayar dalam 12 bulan pertama setelah peluncuran.

### KPI Produk
| Metrik | Target |
|---|---|
| Inbox placement rate (rata-rata pelanggan) | ≥ 90% |
| Hard bounce rate setelah validasi | < 2% |
| Spam complaint rate | < 0,1% |
| Akurasi validasi email | ≥ 95% |
| Waktu pengiriman OTP (p95) | < 10 detik |
| Uptime SMTP Gateway | ≥ 99,9% |
| Waktu dari daftar → kampanye pertama terkirim | < 30 menit |

## 4. Persona Pengguna

| Persona | Kebutuhan Utama |
|---|---|
| **Sales Development Rep (SDR)** | Cari prospek, kirim cold email + follow-up otomatis, lihat siapa yang membalas |
| **Marketing Manager** | Broadcast newsletter/promo ke ribuan kontak, analitik kampanye |
| **Pemilik UKM / Founder** | Tool sederhana, murah, satu dashboard |
| **Developer / Tim IT** | API & SMTP relay untuk email transaksional, OTP, dan SMS |
| **Customer Support Lead** | Auto-reply & forward email ke tim yang tepat |
| **Agency** | Kelola banyak klien/workspace dan domain terpisah |

## 5. Ruang Lingkup

### In Scope (MVP + Fase lanjutan)
Seluruh 11 fitur pada Bagian 6.

### Out of Scope (versi 1)
- CRM penuh (pipeline deal, forecasting) — cukup integrasi ke CRM eksternal.
- Pembelian/penjualan database email pihak ketiga.
- Scraping dari platform yang melarangnya di Terms of Service (mis. LinkedIn yang login-only).
- Pengiriman WhatsApp (dipertimbangkan di roadmap).

---

## 6. Kebutuhan Fungsional

Prioritas: **P0** = wajib MVP, **P1** = fase 2, **P2** = fase 3.

### F1. Pencarian Email Perusahaan B2B (Email Finder / Scraping) — P0

**Deskripsi:** Menemukan alamat email bisnis berdasarkan domain, nama perusahaan, atau kriteria industri dari sumber yang dapat diakses publik.

**User story:** Sebagai SDR, saya ingin memasukkan domain `perusahaan.co.id` dan mendapatkan daftar email karyawan beserta jabatannya, agar saya bisa langsung menyusun daftar prospek.

**Kebutuhan:**
- Input: domain tunggal, daftar domain (upload CSV), nama perusahaan, atau nama orang + domain.
- Sumber data: halaman publik website perusahaan (kontak, tim, karir), WHOIS publik, direktori bisnis publik, metadata halaman, dan pola email yang terdeteksi.
- **Pattern detection:** menebak format email perusahaan (`nama.belakang@`, `n.belakang@`, `nama@`) lalu memverifikasinya lewat F2.
- Pencarian berdasarkan filter: industri, lokasi, ukuran perusahaan, jabatan (bila data tersedia).
- Output: nama, email, jabatan, sumber URL, skor kepercayaan (0–100), tanggal ditemukan.
- Ekspor ke CSV/XLSX dan simpan langsung ke **Daftar Kontak**.
- Ekstensi browser (P1): ambil email dari website yang sedang dibuka.

**Aturan & batasan:**
- Patuhi `robots.txt`, rate limit per domain, dan User-Agent yang jujur.
- Hanya ambil email dengan tujuan bisnis; abaikan data pribadi sensitif.
- Simpan **sumber data** setiap kontak untuk kebutuhan audit dan permintaan hapus data.
- Daftar *suppression* global: domain/email yang meminta tidak dihubungi tidak akan muncul lagi.

**Kriteria penerimaan:**
- Pencarian 1 domain selesai < 30 detik (rata-rata).
- Setiap hasil memiliki skor kepercayaan dan sumber.
- Pencarian massal (≤ 1.000 domain) berjalan sebagai *background job* dengan notifikasi selesai.

### F2. Validasi Email Aktif / Tidak — P0

**Deskripsi:** Memeriksa apakah alamat email valid dan dapat menerima email sebelum dikirimi.

**Tahapan pemeriksaan:**
1. Validasi sintaks (RFC 5322).
2. Pengecekan domain & DNS (MX record ada?).
3. Deteksi email *disposable*/sementara.
4. Deteksi *role-based* (`info@`, `admin@`, `sales@`) — ditandai, tidak otomatis dibuang.
5. Deteksi email gratis (gmail, yahoo, dll.).
6. SMTP handshake (RCPT TO) tanpa mengirim email.
7. Deteksi *catch-all* domain.
8. Pengecekan spam trap & daftar suppression internal.

**Status hasil:** `Valid`, `Invalid`, `Catch-all (Risky)`, `Unknown`, `Disposable`, `Role-based`.

**Kebutuhan:**
- Validasi tunggal (real-time, < 3 detik) dan massal (upload CSV hingga 1 juta baris).
- API validasi untuk dipakai di form pendaftaran pelanggan.
- Validasi otomatis sebelum kampanye dikirim (dapat diatur: blokir `Invalid`, peringatan `Risky`).
- Re-validasi berkala kontak yang lama tidak aktif (mis. > 90 hari).

**Kriteria penerimaan:**
- Akurasi ≥ 95% pada dataset uji.
- Kontak `Invalid` tidak pernah ikut terkirim pada kampanye.

### F3. Broadcast Follow-up Bertingkat (Multi-Layer Funnel / Sequence) — P0

**Deskripsi:** Membuat rangkaian email otomatis dengan jumlah tahap (layer) yang bisa ditentukan pengguna dan percabangan berdasarkan perilaku penerima.

**User story:** Sebagai SDR, saya ingin membuat sequence 5 tahap: email perkenalan → follow-up 3 hari kemudian jika tidak dibalas → email studi kasus jika dibuka tapi tidak dibalas → dst., dan berhenti otomatis saat prospek membalas.

**Kebutuhan:**
- **Jumlah layer fleksibel:** 1 hingga N tahap (default batas 20 tahap per sequence, dapat diatur per paket).
- **Visual funnel builder** (drag & drop) dengan node:
  - *Email* (kirim email/template),
  - *Wait* (tunggu X jam/hari, atau hingga hari & jam tertentu),
  - *Condition* (cabang IF/ELSE),
  - *Action* (tag kontak, pindah sequence, kirim webhook, kirim SMS, notifikasi tim).
- **Kondisi pemicu:** dibuka / tidak dibuka, link diklik / tidak, dibalas / tidak, bounce, unsubscribe, tag tertentu, nilai field kustom.
- **Auto-stop:** sequence berhenti untuk kontak yang membalas, unsubscribe, bounce, atau ditandai "Tertarik".
- Personalisasi dengan variabel: `{{first_name}}`, `{{company}}`, `{{jabatan}}`, field kustom, dan *fallback* value.
- **Spintax & variasi konten** untuk mengurangi pola spam.
- **A/B testing** per tahap (subjek, isi, waktu kirim) dengan pemenang otomatis.
- Jadwal kirim berdasarkan zona waktu penerima & jam kerja (mis. Senin–Jumat 08.00–17.00).
- Laporan per tahap funnel: terkirim, dibuka, diklik, dibalas, konversi, drop-off.

**Kriteria penerimaan:**
- Pengguna dapat membuat sequence dengan ≥ 10 tahap dan ≥ 2 cabang kondisi.
- Kontak yang membalas berhenti menerima email berikutnya dalam ≤ 5 menit setelah balasan diterima.

### F4. Layanan OTP via Email Bisnis — P1

**Deskripsi:** Pelanggan dapat memakai email bisnis mereka (domain sendiri) sebagai pengirim kode OTP untuk aplikasi/website mereka.

**Kebutuhan:**
- API REST: `POST /v1/otp/send` dan `POST /v1/otp/verify`.
- Konfigurasi: panjang kode (4–8 digit), alfanumerik/numerik, masa berlaku (default 5 menit), maksimal percobaan verifikasi.
- Template OTP yang dapat disesuaikan (logo, warna, bahasa).
- Kirim dari alamat domain pelanggan, mis. `no-reply@perusahaananda.com`.
- **Jalur prioritas (priority queue)** terpisah dari email massal agar OTP tidak tertunda.
- *Fallback* otomatis ke SMS (F11) bila email tidak diterima/diverifikasi dalam waktu tertentu (opsional).
- Keamanan: rate limit per penerima & per IP, kode disimpan dalam bentuk hash, satu kali pakai, log audit.
- SDK sederhana (JavaScript, PHP, Python) — P2.

**Kriteria penerimaan:**
- p95 waktu kirim OTP < 10 detik.
- Kode kedaluwarsa tidak dapat diverifikasi; percobaan melebihi batas memblokir sementara.

### F5. Wajib Email Bisnis — P0

**Deskripsi:** Hanya email/domain bisnis terverifikasi yang boleh digunakan sebagai akun dan pengirim.

**Kebutuhan:**
- Pendaftaran akun ditolak jika memakai domain email gratis (gmail.com, yahoo.com, outlook.com, hotmail.com, dan daftar lain yang diperbarui berkala).
- Verifikasi kepemilikan domain via DNS (TXT record).
- **Wizard autentikasi domain** wajib sebelum mengirim: SPF, DKIM, DMARC, serta *custom tracking domain* (CNAME). Dukungan BIMI (P2).
- Pengecekan status DNS otomatis dan peringatan jika konfigurasi rusak.
- Verifikasi data perusahaan (nama, alamat, NPWP/NIB opsional) untuk paket volume tinggi.

**Kriteria penerimaan:**
- Akun dengan domain gratis tidak bisa mendaftar.
- Kampanye tidak bisa dijalankan sebelum SPF & DKIM lulus.

### F6. SMTP Gateway — P0

**Deskripsi:** Infrastruktur relay SMTP dan API untuk pengiriman email (marketing maupun transaksional).

**Kebutuhan:**
- **Outbound SMTP relay** (port 587/465 TLS, 2525 alternatif) dengan kredensial SMTP per pengguna/aplikasi.
- **HTTP API** untuk mengirim email (JSON) + webhook event (delivered, opened, clicked, bounced, complained, unsubscribed).
- **Bring Your Own SMTP:** hubungkan akun SMTP/IMAP sendiri (Google Workspace, Microsoft 365, Zoho, cPanel) via OAuth atau kredensial.
- **Multi-provider routing:** dukungan provider upstream (Amazon SES, SendGrid, Mailgun, Postmark, server MTA sendiri) dengan failover otomatis.
- Pool IP: *shared IP* (default) dan *dedicated IP* (paket tinggi), lengkap dengan **IP warm-up** otomatis.
- Rate limiting & throttling per domain penerima (mis. batasi kiriman ke gmail.com per jam).
- Antrian (queue) dengan retry eksponensial untuk soft bounce.
- Penanganan bounce & feedback loop (FBL) otomatis → masuk suppression list.
- Log pengiriman lengkap yang dapat dicari (retensi 30 hari default).

**Kriteria penerimaan:**
- Uptime ≥ 99,9%.
- Failover ke provider cadangan < 1 menit saat provider utama gagal.

### F7. Mass Email / Kampanye Massal — P0

**Deskripsi:** Mengirim satu kampanye ke daftar kontak dalam jumlah besar.

**Kebutuhan:**
- Manajemen kontak: import CSV/XLSX, pemetaan kolom, deduplikasi, segmen, tag, field kustom.
- Editor email: drag & drop, editor HTML, plain-text, dan template siap pakai (bahasa Indonesia & Inggris).
- Preview di desktop/mobile, kirim email uji, dan **spam score checker** sebelum kirim.
- Penjadwalan, pengiriman bertahap (throttling) dan **rotasi akun pengirim** (beberapa mailbox/domain bergantian).
- Tautan **unsubscribe satu klik** wajib (header `List-Unsubscribe` & `List-Unsubscribe-Post`).
- Pelacakan open, click, reply, bounce, unsubscribe.
- Dashboard laporan kampanye & ekspor laporan.

**Kriteria penerimaan:**
- Mampu memproses kampanye 100.000 penerima tanpa kegagalan antrian.
- Email tanpa link unsubscribe tidak dapat dikirim untuk kampanye marketing.

### F8. Format Email Bisnis Unik (Alias & Mailbox per Kampanye) — P1

**Deskripsi:** Membuat alamat email bisnis unik sesuai kebutuhan, mis. per kampanye, per sales, atau per pelanggan, untuk pelacakan dan branding.

**Kebutuhan:**
- Generator alamat dengan pola yang bisa diatur, mis.:
  - `{nama}.{divisi}@domain.com` → `budi.sales@perusahaan.com`
  - `promo-{kode_kampanye}@domain.com`
  - `reply+{id_kontak}@domain.com` (plus-addressing untuk melacak balasan per kontak)
- Pembuatan alias/mailbox massal pada domain pelanggan.
- Setiap alias dapat diarahkan ke inbox terpadu, ke orang tertentu (F10), atau ke aturan auto-reply (F9).
- Validasi agar alias tidak bentrok dan tetap mengikuti format perusahaan.
- *Sender identity* lengkap: nama tampilan, tanda tangan (signature) HTML, foto/avatar.

**Kriteria penerimaan:**
- Balasan ke alamat `reply+{id}` otomatis terhubung ke kontak & kampanye yang benar.

### F9. Balas Email Otomatis (Auto-Reply) — P1

**Deskripsi:** Membalas email masuk secara otomatis berdasarkan aturan atau AI.

**Kebutuhan:**
- **Unified Inbox:** semua balasan dari seluruh mailbox/alias dalam satu tampilan.
- Rule engine: IF (pengirim, subjek, kata kunci, alias tujuan, jam kerja, tag kontak) THEN (balas dengan template X).
- Mode di luar jam kerja / libur (out-of-office).
- **Klasifikasi balasan otomatis (AI):** Tertarik, Tidak tertarik, Minta info, Out-of-office, Salah orang, Unsubscribe.
- **Draf balasan AI** yang dapat disetujui manusia sebelum terkirim (*human-in-the-loop*) atau dikirim otomatis (opsional).
- Pencegahan *loop*: tidak membalas email `auto-submitted`, bounce, atau alamat no-reply; batasi 1 auto-reply per pengirim per periode.
- Balasan "Unsubscribe/stop" otomatis masuk suppression list.

**Kriteria penerimaan:**
- Auto-reply terkirim ≤ 2 menit setelah email masuk.
- Tidak terjadi loop auto-reply antar dua sistem otomatis.

### F10. Forward Email Otomatis — P1

**Deskripsi:** Meneruskan email masuk ke penerima lain berdasarkan aturan.

**Kebutuhan:**
- Aturan forward berdasarkan: alias tujuan, pengirim/domain, kata kunci, klasifikasi AI (mis. "Tertarik" → forward ke manajer sales).
- Tujuan forward: email lain, beberapa penerima (round-robin antar sales), webhook, Slack/Telegram (P2), atau CRM.
- Opsi: sertakan lampiran, tambahkan catatan/konteks kontak, ubah subjek.
- Forward menggunakan SRS (Sender Rewriting Scheme) agar tetap lolos SPF/DMARC.
- Log forward & status pengiriman.

**Kriteria penerimaan:**
- Email terforward ≤ 2 menit dan tidak ditolak karena kegagalan SPF/DMARC.

### F11. Kirim SMS via SMTP (Email-to-SMS Bridge) — P1

**Deskripsi:** Mengirim SMS dengan cara mengirim email ke alamat khusus; sistem meneruskannya ke SMS gateway pihak ketiga.

**Cara kerja:**
```
Aplikasi pelanggan ──SMTP──► 628123456789@sms.balasemail.com
                                   │
                         Parser (nomor, isi, pengirim)
                                   │
                    Router SMS Gateway (provider + failover)
                                   │
                   Twilio / Vonage / Zenziva / Wavecell / dll.
                                   │
                              HP penerima
```

**Kebutuhan:**
- Format alamat: `{nomor_internasional}@sms.balasemail.com`; isi SMS diambil dari subjek atau body plain-text (dapat diatur).
- Hanya pengirim terotorisasi (alamat terdaftar + SMTP auth / token di header) yang boleh memicu SMS.
- Konektor provider SMS: Twilio, Vonage, MessageBird, Infobip, serta provider lokal Indonesia (mis. Zenziva, provider masking/OTP lokal). Pelanggan bisa memasukkan API key provider sendiri.
- Dukungan Sender ID / masking sesuai regulasi operator.
- Pemotongan & penggabungan pesan panjang (multi-part SMS), dukungan Unicode.
- Laporan status (delivered/failed) dikembalikan via webhook atau email notifikasi.
- SMS juga dapat dipicu dari node *Action* di funnel (F3) dan sebagai fallback OTP (F4).
- Sistem kredit/saldo SMS per akun.

**Kriteria penerimaan:**
- Email ke alamat SMS valid menghasilkan SMS terkirim ≤ 30 detik (tergantung provider).
- Email dari pengirim tidak terotorisasi ditolak dan dicatat.

---

## 7. Fitur Pendukung (Cross-cutting)

| Fitur | Prioritas |
|---|---|
| Multi-workspace & manajemen tim (role: Owner, Admin, Member, Viewer) | P0 |
| Billing & paket langganan (kuota email, validasi, SMS) — pembayaran lokal (VA, QRIS, e-wallet) & kartu kredit | P0 |
| Dashboard analitik terpadu | P0 |
| Deliverability center: monitoring blacklist, reputasi domain, DMARC report, inbox placement test | P1 |
| Email warm-up otomatis untuk mailbox baru | P1 |
| Public REST API + API key + webhook | P1 |
| Integrasi: HubSpot, Salesforce, Pipedrive, Google Sheets, Zapier/Make | P2 |
| Audit log seluruh aktivitas | P1 |
| Multi-bahasa UI (Indonesia, Inggris) | P0 |

## 8. Kepatuhan, Etika & Anti-Abuse (Wajib)

Fitur scraping dan email massal berisiko disalahgunakan untuk spam. Produk **harus** dibangun dengan pengaman berikut sejak MVP:

**Regulasi yang dipatuhi:**
- **UU No. 27 Tahun 2022 tentang Pelindungan Data Pribadi (UU PDP)** — Indonesia.
- **UU ITE** dan aturan Kominfo/Komdigi terkait pesan elektronik & SMS.
- **GDPR** (penerima di Uni Eropa), **CAN-SPAM** (AS), **PDPA** (Singapura/Malaysia).
- Persyaratan pengirim massal **Google & Yahoo** (SPF/DKIM/DMARC, one-click unsubscribe, spam rate < 0,3%).

**Kontrol produk:**
- Tautan unsubscribe wajib & diproses ≤ 48 jam (target instan).
- **Suppression list global & per akun** yang tidak bisa dilewati.
- Identitas & alamat fisik pengirim wajib ada di footer email marketing.
- Dasar hukum pemrosesan (*legitimate interest* B2B / consent) dicatat per daftar kontak; pengguna wajib menyetujui Acceptable Use Policy.
- Fitur permintaan hak subjek data: akses, koreksi, dan **hapus data** (termasuk dari hasil scraping).
- Kuota pengiriman bertahap untuk akun baru (mis. 200/hari → naik sesuai reputasi).
- Monitoring otomatis: akun dengan bounce > 5% atau complaint > 0,3% ditangguhkan otomatis untuk ditinjau.
- Larangan konten: phishing, penipuan, judi online, pinjol ilegal, konten dewasa — dicek dengan filter konten.
- KYC untuk paket volume tinggi & dedicated IP.

## 9. Kebutuhan Non-Fungsional

| Aspek | Kebutuhan |
|---|---|
| **Performa** | Throughput ≥ 1 juta email/jam (skala horizontal); API p95 < 300 ms |
| **Ketersediaan** | 99,9% untuk SMTP Gateway & API OTP |
| **Skalabilitas** | Worker pengiriman, scraping, dan validasi dapat diskalakan terpisah |
| **Keamanan** | TLS di semua koneksi; enkripsi kredensial SMTP/API key (AES-256 / KMS); 2FA untuk login; RBAC; proteksi OWASP Top 10 |
| **Privasi** | Data disimpan di region Indonesia/Asia Tenggara; retensi data dapat dikonfigurasi |
| **Observability** | Logging terpusat, metrik, tracing, alert on-call |
| **Backup** | Backup harian, RPO ≤ 24 jam, RTO ≤ 4 jam |

## 10. Arsitektur Tingkat Tinggi (Usulan)

```
┌───────────────┐   ┌──────────────┐   ┌──────────────────────┐
│ Web App (SPA) │──►│  API Gateway │──►│ Core Service (Auth,  │
│ + Extension   │   │  + Rate Limit│   │ Workspace, Billing)  │
└───────────────┘   └──────┬───────┘   └──────────┬───────────┘
                           │                      │
     ┌─────────────┬───────┼─────────┬────────────┼──────────────┐
     ▼             ▼       ▼         ▼            ▼              ▼
 Finder/      Validator  Campaign &  Inbound     OTP Service   SMS Bridge
 Scraper      Service    Sequence    Processor   (priority     (SMTP→SMS
 Workers      (DNS/SMTP) Engine      (IMAP/MX,   queue)        router)
                         (scheduler) auto-reply,
                                     forward, AI)
     └─────────────┴───────┬─────────┴────────────┴──────────────┘
                           ▼
              Message Queue (Redis/RabbitMQ/Kafka)
                           ▼
              SMTP Gateway / MTA (Postfix/Haraka/KumoMTA)
              + Upstream provider (SES, SendGrid, dll.)
                           ▼
              PostgreSQL · Redis · Object Storage · ClickHouse (analitik event)
```

**Usulan stack:** Frontend Next.js/React; Backend Node.js (NestJS) atau Go; PostgreSQL; Redis + BullMQ / RabbitMQ; ClickHouse untuk event tracking; MTA KumoMTA/Haraka/Postfix; LLM API untuk klasifikasi & draf balasan; deploy di Kubernetes/cloud region Jakarta.

## 11. Model Data Inti (Ringkas)

- **Workspace** (id, nama, paket, kuota)
- **User** (id, workspace_id, email bisnis, role, 2FA)
- **Domain** (id, nama, status SPF/DKIM/DMARC, tracking domain)
- **Mailbox / Sender Identity** (id, domain_id, alamat, alias pattern, SMTP/IMAP creds terenkripsi, limit harian)
- **Contact** (id, email, nama, perusahaan, jabatan, status validasi, sumber, dasar hukum, tag, field kustom)
- **List / Segment**
- **Campaign** & **Sequence** (node, edge, kondisi)
- **Message** (id, contact_id, step_id, status, message-id, event timestamps)
- **InboundEmail** (id, mailbox_id, thread, klasifikasi AI)
- **Rule** (tipe: auto-reply / forward, kondisi, aksi)
- **OTP Request** (id, penerima, hash kode, expiry, attempts)
- **SMS Message** (id, nomor, isi, provider, status, biaya)
- **Suppression** (email/domain, alasan, sumber, tanggal)

## 12. Rencana Rilis (Roadmap)

| Fase | Durasi | Cakupan |
|---|---|---|
| **Fase 0 — Fondasi** | Bulan 1–2 | Auth, workspace, wajib email bisnis (F5), autentikasi domain, billing dasar, SMTP Gateway inti (F6) |
| **Fase 1 — MVP Outreach** | Bulan 3–4 | Email finder (F1), validasi (F2), kontak & mass email (F7), sequence multi-layer (F3), unified inbox dasar, compliance kit |
| **Fase 2 — Otomasi** | Bulan 5–6 | Alias unik (F8), auto-reply + AI klasifikasi (F9), auto-forward (F10), OTP API (F4), Email-to-SMS (F11), warm-up, deliverability center |
| **Fase 3 — Skala** | Bulan 7–9 | Dedicated IP, integrasi CRM, SDK, browser extension, agency/white-label, BIMI |

## 13. Paket Harga (Indikatif)

| Paket | Harga/bulan | Email/bulan | Validasi | Pencarian email | Mailbox |
|---|---|---|---|---|---|
| Starter | Rp149.000 | 10.000 | 1.000 | 250 | 2 |
| Growth | Rp499.000 | 100.000 | 10.000 | 2.500 | 10 |
| Business | Rp1.499.000 | 500.000 | 50.000 | 10.000 | 50 |
| Enterprise | Custom | Custom + dedicated IP | Custom | Custom | Tak terbatas |

OTP dan SMS ditagih terpisah berbasis pemakaian (pay-as-you-go / kredit).

## 14. Risiko & Mitigasi

| Risiko | Dampak | Mitigasi |
|---|---|---|
| Platform dipakai spammer | IP/domain di-blacklist, reputasi rusak | KYC, kuota bertahap, monitoring otomatis, suspensi |
| Scraping melanggar ToS/hukum | Tuntutan hukum, blokir | Hanya sumber publik, patuh robots.txt, fitur hapus data, review legal |
| Validasi SMTP diblokir server penerima | Akurasi turun | Rotasi IP validasi, cache hasil, status `Unknown` yang jujur |
| Provider SMS/email upstream down | OTP gagal | Multi-provider failover |
| Auto-reply AI salah konteks | Citra pelanggan buruk | Mode persetujuan manusia sebagai default |
| Regulasi SMS masking di Indonesia | SMS tidak terkirim | Bekerja sama dengan provider resmi berlisensi |

## 15. Pertanyaan Terbuka

1. Apakah MTA dibangun sendiri sejak awal, atau memakai provider upstream (SES/SendGrid) dulu lalu migrasi?
2. Target pasar awal: hanya Indonesia atau langsung Asia Tenggara?
3. Apakah auto-reply AI boleh mengirim tanpa persetujuan manusia pada paket tertentu?
4. Provider SMS lokal mana yang menjadi mitra utama?
5. Apakah perlu fitur white-label untuk agency di fase awal?
6. Berapa lama retensi data hasil scraping yang tidak pernah digunakan?

## 16. Glosarium

- **SPF / DKIM / DMARC** — standar autentikasi email untuk membuktikan pengirim sah.
- **Catch-all** — domain yang menerima semua alamat, sehingga validasi tidak pasti.
- **Sequence / Funnel** — rangkaian email otomatis bertahap.
- **Suppression list** — daftar alamat yang tidak boleh dikirimi.
- **IP warm-up** — menaikkan volume kirim secara bertahap untuk membangun reputasi.
- **SRS** — Sender Rewriting Scheme, teknik agar forward email tetap lolos SPF.
- **MTA** — Mail Transfer Agent, server pengirim email.
