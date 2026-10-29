// BalasEmail dashboard — a small dependency-free SPA over the REST API.
"use strict";

// ---------- helpers ----------
const $app = document.getElementById("app");
const store = {
  get token() { try { return localStorage.getItem("bm_token") || ""; } catch { return ""; } },
  set token(v) { try { v ? localStorage.setItem("bm_token", v) : localStorage.removeItem("bm_token"); } catch {} },
};
let me = null;

// h builds DOM nodes; strings become text nodes, so API data is never parsed as HTML.
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "class") el.className = v;
    else if (k === "style") el.style.cssText = v; // CSSOM is allowed under our CSP; style attributes are not
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

function toast(msg, isErr) {
  const t = h("div", { class: "t" + (isErr ? " err" : "") }, msg);
  document.getElementById("toast").append(t);
  setTimeout(() => t.remove(), isErr ? 6000 : 3000);
}

// Indonesian messages for common API error codes; anything else shows the API message.
const errorText = {
  business_email_required: "Gunakan email perusahaan untuk mendaftar (bukan Gmail/Yahoo/Outlook).",
  disposable_email: "Email sementara tidak diterima.",
  invalid_email: "Alamat email tidak valid.",
  weak_password: "Password minimal 10 karakter.",
  missing_fields: "Nama perusahaan dan nama lengkap wajib diisi.",
  email_taken: "Email ini sudah terdaftar. Silakan masuk.",
  invalid_credentials: "Email atau password salah.",
  rate_limited: "Terlalu banyak percobaan. Coba lagi sebentar lagi.",
  invalid_domain: "Nama domain tidak valid.",
  business_domain_required: "Hanya domain milik perusahaan Anda yang bisa dipakai.",
  domain_exists: "Domain ini sudah ditambahkan.",
  domain_claimed: "Domain ini sudah diverifikasi oleh workspace lain.",
  domain_not_registered: "Domain pengirim belum ditambahkan. Tambahkan di menu Domain.",
  domain_not_verified: "Domain pengirim belum terverifikasi. Pasang record DNS lalu klik Cek DNS.",
  spf_missing: "Record SPF domain pengirim belum benar.",
  dkim_missing: "Record DKIM domain pengirim belum benar.",
  daily_limit_reached: "Kuota kirim harian sudah habis. Lanjutkan besok atau upgrade paket.",
  no_recipients: "Tidak ada kontak yang cocok dengan target tag kampanye ini.",
  already_launched: "Kampanye ini sudah diluncurkan.",
  not_a_member: "Email uji hanya bisa dikirim ke anggota workspace.",
  insufficient_sms_credits: "Kredit SMS tidak cukup. Isi ulang saldo terlebih dahulu.",
  invalid_phone: "Nomor HP tidak valid.",
  otp_rate_limited: "Terlalu banyak kode diminta untuk penerima ini. Coba lagi nanti.",
  missing_from: "Isi alamat pengirim (di domain terverifikasi) untuk OTP email.",
  invalid_csv: "Baris pertama file harus berisi judul kolom, termasuk kolom 'email'.",
  unsupported_xls: "File Excel lama (.xls) belum didukung. Simpan ulang sebagai Excel Workbook (.xlsx) atau CSV.",
  invalid_xlsx: "File Excel tidak bisa dibaca. Pastikan formatnya .xlsx dan tidak dilindungi password.",
  empty_file: "File tidak berisi alamat email.",
  file_too_large: "Ukuran file maksimal 50MB.",
  too_many_rows: "Maksimal 100.000 baris per impor. Bagi file menjadi beberapa bagian.",
  not_a_company_domain: "Pencarian hanya untuk domain perusahaan.",
  crawl_failed: "Website perusahaan tidak bisa diakses.",
  no_mx: "Domain ini tidak menerima email.",
  not_found: "Tidak ditemukan alamat yang bisa dikirimi untuk orang ini.",
  workspace_suspended: "Workspace ditangguhkan karena reputasi pengiriman. Hubungi support.",
  invalid_url: "URL harus diawali https://",
};

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (store.token) opts.headers.Authorization = "Bearer " + store.token;
  if (body instanceof FormData) opts.body = body;
  else if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
  const res = await fetch(path, opts);
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (res.status === 401 && path !== "/v1/auth/login") { store.token = ""; me = null; route(); }
  if (!res.ok && !(data && "valid" in data)) {
    const err = data && data.error;
    const msg = (err && (errorText[err.code] || err.message)) || ("HTTP " + res.status);
    throw new Error(msg);
  }
  return data;
}

// run wraps an async action with button disabling and error toasts.
async function run(btn, fn) {
  if (btn) btn.disabled = true;
  try { return await fn(); }
  catch (e) { toast(e.message, true); }
  finally { if (btn) btn.disabled = false; }
}

const fmtDate = (s) => s ? new Date(s).toLocaleString("id-ID", { dateStyle: "medium", timeStyle: "short" }) : "—";
const pct = (x) => (x * 100).toFixed(1) + "%";
const ok = (b, yes = "OK", no = "Belum") => h("span", { class: "badge " + (b ? "ok" : "bad") }, b ? yes : no);

const statusClass = {
  valid: "ok", sent: "ok", active: "ok", completed: "ok", interested: "ok",
  risky: "warn", unknown: "warn", queued: "warn", paused: "warn", draft: "", sending: "info", question: "info",
  invalid: "bad", disposable: "bad", failed: "bad", bounced: "bad", stopped: "bad", not_interested: "bad", unsubscribe: "bad",
};
const statusLabel = {
  valid: "valid", invalid: "tidak valid", risky: "berisiko", unknown: "tidak pasti", unverified: "belum dicek", disposable: "sementara",
  draft: "draft", sending: "mengirim", sent: "terkirim", paused: "dijeda", queued: "antre", failed: "gagal", bounced: "bounce",
  active: "aktif", completed: "selesai", stopped: "berhenti", revoked: "dicabut",
  interested: "tertarik", not_interested: "tidak tertarik", question: "bertanya", out_of_office: "sedang cuti",
  wrong_person: "salah orang", unsubscribe: "minta berhenti", other: "lainnya", automated: "otomatis",
  hard_bounce: "hard bounce", complaint: "laporan spam", manual: "manual", erasure_request: "hapus data",
};
const badge = (s) => h("span", { class: "badge " + (statusClass[s] || "") }, statusLabel[s] || s || "—");

function table(cols, rows, emptyText) {
  if (!rows || rows.length === 0) return h("div", { class: "empty" }, emptyText || "Belum ada data.");
  return h("table", {},
    h("thead", {}, h("tr", {}, cols.map((c) => h("th", {}, c.label)))),
    h("tbody", {}, rows.map((r) => h("tr", {}, cols.map((c) => h("td", { class: c.mono ? "mono" : "" }, c.render ? c.render(r) : (r[c.key] ?? "—")))))));
}

// form renders labelled fields and calls onSubmit with their values.
function form(fields, submitLabel, onSubmit, extra) {
  const inputs = {};
  const f = h("form", {},
    h("div", { class: "fields" }, fields.map((fd) => {
      let input;
      if (fd.type === "textarea") input = h("textarea", { name: fd.name, placeholder: fd.placeholder || "", class: fd.code ? "code" : "" });
      else if (fd.type === "select") input = h("select", { name: fd.name }, fd.options.map((o) => h("option", { value: o[0] }, o[1])));
      else input = h("input", { name: fd.name, type: fd.type || "text", placeholder: fd.placeholder || "", required: fd.required, accept: fd.accept });
      if (fd.value != null && fd.type !== "file") input.value = fd.value;
      inputs[fd.name] = input;
      return h("div", { class: fd.full ? "full" : "" }, h("label", {}, fd.label), input, fd.help ? h("div", { class: "help" }, fd.help) : null);
    })),
    h("div", { class: "actions" }, h("button", { class: "primary", type: "submit" }, submitLabel), extra || null));
  f.addEventListener("submit", (e) => {
    e.preventDefault();
    const vals = {};
    for (const [k, el] of Object.entries(inputs)) vals[k] = el.type === "file" ? el.files[0] : el.value.trim();
    run(f.querySelector("button[type=submit]"), () => onSubmit(vals, f));
  });
  return f;
}

const csv = (s) => (s || "").split(",").map((x) => x.trim()).filter(Boolean);
const card = (title, ...kids) => h("div", { class: "card" }, title ? h("h2", {}, title) : null, ...kids);
const page = (title, sub, ...kids) => [h("div", { class: "topbar" }, h("div", {}, h("h1", {}, title), h("p", { class: "sub" }, sub)),
  me ? h("div", { class: "who" }, me.user.full_name || "", h("br"), me.workspace.name) : null), ...kids];

// ---------- pages ----------
const pages = {};

pages.overview = async () => {
  const [domains, campaigns, contacts, inbox] = await Promise.all([
    api("GET", "/v1/domains"), api("GET", "/v1/campaigns?limit=5"), api("GET", "/v1/contacts?limit=200"), api("GET", "/v1/inbox?limit=5"),
  ]);
  const w = me.workspace;
  const ready = domains.data.filter((d) => d.verified && d.spf_ok && d.dkim_ok).length;
  const stat = (label, value, hint) => h("div", { class: "card stat" }, h("div", { class: "label" }, label), h("div", { class: "value" }, value), h("div", { class: "hint" }, hint));
  const steps = [
    ["Tambahkan & verifikasi domain bisnis", ready > 0, "#/domains"],
    ["Impor atau tambah kontak", contacts.data.length > 0, "#/contacts"],
    ["Buat dan luncurkan kampanye", campaigns.data.some((c) => c.status !== "draft"), "#/campaigns"],
  ];
  return page("Ringkasan", "Status workspace dan langkah berikutnya.",
    h("div", { class: "grid cols-4" },
      stat("Domain siap kirim", ready + " / " + domains.data.length, "SPF + DKIM terverifikasi"),
      stat("Kontak", contacts.data.length >= 200 ? "200+" : contacts.data.length, "di workspace ini"),
      stat("Kuota harian", w.daily_send_limit, "email marketing/sequence"),
      stat("Kredit SMS", w.sms_credits, "segmen tersisa")),
    h("div", { class: "grid cols-2" },
      card("Mulai di sini", h("div", { class: "stack" }, steps.map(([t, done, href]) =>
        h("div", { class: "row spread" }, h("a", { href }, t), ok(done, "Selesai", "Belum"))))),
      card("Balasan terbaru", table([
        { label: "Dari", render: (r) => r.from_name || r.from },
        { label: "Subjek", key: "subject" },
        { label: "Klasifikasi", render: (r) => badge(r.classification) },
      ], inbox.data, "Belum ada balasan masuk."))),
    card("Kampanye terbaru", table([
      { label: "Nama", render: (r) => h("a", { href: "#/campaigns/" + r.id }, r.name) },
      { label: "Status", render: (r) => badge(r.status) },
      { label: "Dibuat", render: (r) => fmtDate(r.created_at) },
    ], campaigns.data, "Belum ada kampanye.")));
};

pages.domains = async () => {
  const list = await api("GET", "/v1/domains");
  const box = h("div");
  const render = (domains) => {
    box.replaceChildren(...domains.map((d) => card(null,
      h("div", { class: "row spread" },
        h("div", {}, h("h2", { style: "margin:0" }, d.name), h("div", { class: "help" }, "Dicek: " + fmtDate(d.last_checked_at))),
        h("div", { class: "row" }, ok(d.verified && d.spf_ok && d.dkim_ok, "Siap kirim", "Belum siap"),
          h("button", { onclick: (e) => run(e.target, async () => { await api("POST", "/v1/domains/" + d.id + "/check"); toast("DNS dicek ulang"); route(); }) }, "Cek DNS"))),
      h("p", { class: "help" }, "Pasang record berikut di DNS domain Anda:"),
      table([
        { label: "Fungsi", key: "purpose" },
        { label: "Tipe", key: "type" },
        { label: "Host", key: "host", mono: true },
        { label: "Nilai", key: "value", mono: true },
        { label: "Status", render: (r) => ok(r.ok, "OK", r.required ? "Belum" : "Opsional") },
      ], d.dns_records))));
    if (domains.length === 0) box.replaceChildren(card(null, h("div", { class: "empty" }, "Belum ada domain. Tambahkan domain perusahaan Anda di atas.")));
  };
  render(list.data);
  return page("Domain pengirim", "Hanya domain bisnis yang terverifikasi yang bisa dipakai mengirim (SPF, DKIM, DMARC).",
    card("Tambah domain", form([{ name: "name", label: "Nama domain", placeholder: "perusahaan.co.id", required: true }], "Tambah",
      async (v) => { await api("POST", "/v1/domains", { name: v.name }); toast("Domain ditambahkan"); route(); })),
    box);
};

// importResult holds the last import report so it survives the page refresh after importing.
let importResult = null;

// rowError translates the parser's per-row messages.
function rowError(msg) {
  return msg
    .replace(/^invalid email "(.*)"$/, 'alamat email tidak valid: "$1"')
    .replace(/^duplicate email (.*)$/, "email ganda di file: $1")
    .replace(/^no email address found in (.*)$/, "tidak ada alamat email di $1");
}

function importReport() {
  const r = importResult;
  importResult = null;
  if (!r) return null;
  return h("div", { class: "callout" + (r.skipped ? " warn" : ""), style: "margin:14px 0 0" },
    h("b", {}, `Hasil impor (${r.format.toUpperCase()}): `), `${r.imported} kontak baru, ${r.updated} diperbarui, ${r.skipped} dilewati.`,
    r.errors && r.errors.length ? h("ul", { style: "margin:6px 0 0;padding-left:18px" },
      r.errors.slice(0, 8).map((e) => h("li", {}, `Baris ${e.row}: ${rowError(e.error)}`)),
      r.errors.length > 8 ? h("li", {}, `…dan ${r.errors.length - 8} baris lainnya`) : null) : null);
}

pages.contacts = async () => {
  const q = new URLSearchParams(location.hash.split("?")[1] || "");
  const list = await api("GET", "/v1/contacts?limit=100&q=" + encodeURIComponent(q.get("q") || "") + "&tag=" + encodeURIComponent(q.get("tag") || ""));
  const search = h("form", { class: "row", style: "flex-wrap:nowrap" },
    h("input", { name: "q", placeholder: "Cari email / perusahaan", value: q.get("q") || "", style: "width:220px" }),
    h("input", { name: "tag", placeholder: "Tag", value: q.get("tag") || "", style: "width:110px" }),
    h("button", { type: "submit" }, "Cari"));
  search.addEventListener("submit", (e) => { e.preventDefault(); location.hash = "#/contacts?" + new URLSearchParams(new FormData(search)); });
  return page("Kontak", "Prospek dan pelanggan. Hapus kontak = hak hapus data (UU PDP).",
    h("div", { class: "grid cols-2" },
      card("Tambah kontak", form([
        { name: "email", label: "Email", required: true }, { name: "first_name", label: "Nama depan" },
        { name: "company", label: "Perusahaan" }, { name: "title", label: "Jabatan" },
        { name: "phone", label: "No. HP" }, { name: "tags", label: "Tag (pisahkan koma)", placeholder: "b2b, jakarta" },
      ], "Simpan", async (v) => {
        await api("POST", "/v1/contacts", { email: v.email, first_name: v.first_name, company: v.company, title: v.title, phone: v.phone, tags: csv(v.tags) });
        toast("Kontak disimpan"); route();
      })),
      card("Impor dari file", form([
        { name: "file", label: "File Excel (.xlsx), CSV, atau TXT", type: "file", accept: ".xlsx,.csv,.txt,.tsv,text/csv,text/plain,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", required: true, full: true,
          help: "Excel/CSV: baris pertama berisi judul kolom, wajib ada kolom email. Kolom yang dikenali: nama depan, nama belakang, perusahaan, jabatan, no hp, tags; kolom lain jadi field kustom. TXT: satu email per baris, atau format Nama <email>." },
        { name: "tags", label: "Tag untuk semua baris", placeholder: "import-nov", full: true },
      ], "Impor", async (v) => {
        const fd = new FormData(); fd.append("file", v.file); fd.append("tags", v.tags);
        const r = await api("POST", "/v1/contacts/import", fd);
        importResult = r;
        toast(`Impor ${r.format.toUpperCase()}: ${r.imported} baru, ${r.updated} diperbarui, ${r.skipped} dilewati`); route();
      }, h("button", { type: "button", onclick: (e) => run(e.target, async () => { await api("POST", "/v1/contacts/validate", {}); toast("Validasi massal dijadwalkan"); }) }, "Validasi semua")),
        h("div", { class: "samples" }, h("span", { class: "help" }, "Unduh contoh file: "),
          h("a", { href: "/static/contoh/kontak-contoh.xlsx", download: "kontak-contoh.xlsx" }, "Excel (.xlsx)"),
          h("a", { href: "/static/contoh/kontak-contoh.csv", download: "kontak-contoh.csv" }, "CSV"),
          h("a", { href: "/static/contoh/kontak-contoh.txt", download: "kontak-contoh.txt" }, "TXT")),
        importReport())),
    card(null, h("div", { class: "row spread", style: "margin-bottom:10px" }, h("h2", { style: "margin:0" }, "Daftar kontak"), search),
      table([
        { label: "Email", key: "email" },
        { label: "Nama", render: (r) => [r.first_name, r.last_name].filter(Boolean).join(" ") || "—" },
        { label: "Perusahaan", key: "company" },
        { label: "Tag", render: (r) => r.tags.map((t) => h("span", { class: "badge", style: "margin-right:4px" }, t)) },
        { label: "Validasi", render: (r) => badge(r.validation_status) },
        { label: "Langganan", render: (r) => r.unsubscribed_at ? h("span", { class: "badge bad" }, "berhenti") : h("span", { class: "badge ok" }, "aktif") },
        { label: "", render: (r) => h("button", { class: "sm danger", onclick: (e) => {
          if (!confirm("Hapus " + r.email + " dan blokir dari pengiriman berikutnya?")) return;
          run(e.target, async () => { await api("DELETE", "/v1/contacts/" + r.id + "?suppress=true"); toast("Kontak dihapus"); route(); });
        } }, "Hapus") },
      ], list.data, "Belum ada kontak.")));
};

pages.finder = async () => {
  const out = h("div");
  const showResult = (rows) => out.replaceChildren(card("Hasil", table([
    { label: "Email", key: "email" }, { label: "Nama", render: (r) => [r.first_name, r.last_name].filter(Boolean).join(" ") || "—" },
    { label: "Metode", key: "method" }, { label: "Keyakinan", render: (r) => r.confidence + "%" },
    { label: "Sumber", render: (r) => r.source_url ? h("a", { href: r.source_url, target: "_blank", rel: "noopener noreferrer" }, "lihat") : "—" },
  ], rows, "Tidak ada email ditemukan.")));
  return page("Cari email B2B", "Hanya dari halaman publik website perusahaan (patuh robots.txt) dan pola nama yang diverifikasi.",
    h("div", { class: "grid cols-2" },
      card("Berdasarkan domain", form([
        { name: "domain", label: "Domain perusahaan", placeholder: "perusahaan.co.id", required: true, full: true },
        { name: "tags", label: "Simpan ke kontak dengan tag (opsional)", full: true },
      ], "Cari", async (v) => {
        const r = await api("POST", "/v1/finder/domain", { domain: v.domain, save_contacts: !!v.tags, tags: csv(v.tags) });
        showResult(r.data); if (r.saved) toast(r.saved + " kontak disimpan");
      })),
      card("Berdasarkan nama orang", form([
        { name: "first_name", label: "Nama depan", required: true }, { name: "last_name", label: "Nama belakang" },
        { name: "domain", label: "Domain perusahaan", required: true, full: true },
      ], "Cari", async (v) => showResult([await api("POST", "/v1/finder/person", v)])))),
    out);
};

pages.validate = async () => {
  const out = h("div");
  return page("Validasi email", "Cek sintaks, MX, disposable, role-based, dan (jika aktif) probe SMTP.",
    card("Cek satu email", form([{ name: "email", label: "Email", required: true, full: true }], "Validasi", async (v) => {
      const r = await api("POST", "/v1/validations", { email: v.email });
      out.replaceChildren(card("Hasil: " + r.email,
        h("div", { class: "row", style: "margin-bottom:12px" }, badge(r.status), h("span", {}, "Alasan: " + r.reason), h("span", {}, "Skor: " + r.score)),
        table([{ label: "Pemeriksaan", key: "k" }, { label: "Hasil", key: "v" }],
          Object.entries(r.checks).map(([k, val]) => ({ k, v: String(val) })))));
    })), out);
};

const campaignTemplate = `<p>{Halo|Hai} {{first_name|"Bapak/Ibu"}},</p>
<p>Kami membantu perusahaan seperti {{company|"Anda"}} ...</p>
<p><a href="https://perusahaan-anda.co.id">Pelajari lebih lanjut</a></p>`;

pages.campaigns = async (id) => {
  if (id) return campaignDetail(id);
  const list = await api("GET", "/v1/campaigns");
  return page("Kampanye", "Email massal ke segmen kontak (berdasarkan tag), dengan tracking dan unsubscribe otomatis.",
    card("Buat kampanye", form([
      { name: "name", label: "Nama kampanye", required: true },
      { name: "from_email", label: "Dari (email di domain terverifikasi)", placeholder: "sales@perusahaan.co.id", required: true },
      { name: "from_name", label: "Nama pengirim" },
      { name: "tag_filter", label: "Target tag (kosong = semua kontak)", placeholder: "b2b" },
      { name: "subject", label: "Subjek", placeholder: "{Halo|Hai} {{first_name}}, ...", required: true, full: true },
      { name: "html", label: "Isi (HTML)", type: "textarea", value: campaignTemplate, full: true,
        help: "Variabel: {{first_name}}, {{company}}, {{title}}, field kustom; fallback {{x|\"default\"}}; spintax {a|b}. Footer unsubscribe ditambahkan otomatis." },
      { name: "send_per_minute", label: "Kecepatan (email/menit)", type: "number", value: "60" },
    ], "Simpan draft", async (v) => {
      const c = await api("POST", "/v1/campaigns", { ...v, tag_filter: csv(v.tag_filter), send_per_minute: Number(v.send_per_minute) || 60 });
      toast("Draft dibuat"); location.hash = "#/campaigns/" + c.id;
    })),
    card("Semua kampanye", table([
      { label: "Nama", render: (r) => h("a", { href: "#/campaigns/" + r.id }, r.name) },
      { label: "Dari", key: "from_email" }, { label: "Target", render: (r) => r.tag_filter.join(", ") || "semua" },
      { label: "Status", render: (r) => badge(r.status) }, { label: "Dibuat", render: (r) => fmtDate(r.created_at) },
    ], list.data, "Belum ada kampanye.")));
};

async function campaignDetail(id) {
  const [c, st] = await Promise.all([api("GET", "/v1/campaigns/" + id), api("GET", "/v1/campaigns/" + id + "/stats")]);
  const stat = (label, value, hint) => h("div", { class: "card stat" }, h("div", { class: "label" }, label), h("div", { class: "value" }, value), h("div", { class: "hint" }, hint || ""));
  const preview = h("iframe", { sandbox: "", style: "width:100%;height:260px;border:1px solid var(--border);border-radius:8px;background:#fff" });
  preview.srcdoc = c.html || "<pre>" + (c.text || "") + "</pre>";
  return page(c.name, "Dari " + (c.from_name ? c.from_name + " <" + c.from_email + ">" : c.from_email) + " · target: " + (c.tag_filter.join(", ") || "semua kontak"),
    h("div", { class: "row", style: "margin-bottom:16px" }, badge(c.status),
      c.status === "draft" || c.status === "paused" ? h("button", { class: "primary", onclick: (e) => {
        if (!confirm("Luncurkan kampanye ke semua kontak yang cocok?")) return;
        run(e.target, async () => { const r = await api("POST", "/v1/campaigns/" + id + "/launch"); toast(`${r.queued} email diantrikan` + (r.note ? " — " + r.note : "")); route(); });
      } }, "Luncurkan") : null,
      h("button", { onclick: (e) => run(e.target, async () => { await api("POST", "/v1/campaigns/" + id + "/test", { to: me.user.email }); toast("Email uji dikirim ke " + me.user.email); }) }, "Kirim uji ke saya"),
      h("button", { onclick: () => route() }, "Muat ulang statistik"),
      h("a", { href: "#/campaigns" }, "← Semua kampanye")),
    h("div", { class: "grid cols-4" },
      stat("Terkirim", st.sent, st.queued + " dalam antrean"),
      stat("Dibuka", pct(st.open_rate), st.opened + " penerima"),
      stat("Diklik", pct(st.click_rate), st.clicked + " penerima"),
      stat("Bounce", pct(st.bounce_rate), st.bounced + " · " + st.unsubscribed + " berhenti langganan")),
    card("Subjek", h("div", { class: "mono" }, c.subject)),
    card("Pratinjau isi (sebelum personalisasi)", preview));
}

const sequenceTemplate = JSON.stringify({
  start: "e1",
  settings: { stop_on_reply: true },
  nodes: [
    { id: "e1", type: "email", subject: "{Halo|Hai} {{first_name}}", text: "Perkenalan singkat ...", next: "w1", variants: [{ subject: "Kerja sama dengan {{company}}?" }] },
    { id: "w1", type: "wait", wait: { days: 3 }, next: "c1" },
    { id: "c1", type: "condition", condition: { type: "opened" }, yes: "e2", no: "e3" },
    { id: "e2", type: "email", subject: "Studi kasus untuk {{company}}", text: "...", thread_reply: true, next: "w2" },
    { id: "w2", type: "wait", wait: { days: 2 }, next: "a1" },
    { id: "a1", type: "action", action: { type: "add_tag", tags: ["hot"] } },
    { id: "e3", type: "email", subject: "Masih relevan?", text: "...", next: "end" },
    { id: "end", type: "end" },
  ],
}, null, 2);

const nodeLabel = { email: "✉️ Email", wait: "⏳ Tunggu", condition: "🔀 Kondisi", action: "⚡ Aksi", end: "🏁 Selesai" };
function describeNode(n) {
  switch (n.type) {
    case "email": return n.subject + (n.variants && n.variants.length ? ` (+${n.variants.length} varian A/B)` : "");
    case "wait": return [n.wait.days && n.wait.days + " hari", n.wait.hours && n.wait.hours + " jam", n.wait.minutes && n.wait.minutes + " menit"].filter(Boolean).join(" ");
    case "condition": return `${n.condition.type}${n.condition.tag ? " " + n.condition.tag : ""} → ya: ${n.yes || "selesai"}, tidak: ${n.no || "selesai"}`;
    case "action": return n.action.type + (n.action.tags ? " " + n.action.tags.join(", ") : "");
    default: return "";
  }
}

pages.sequences = async (id) => {
  if (id) return sequenceDetail(id);
  const list = await api("GET", "/v1/sequences");
  return page("Sequence follow-up", "Funnel multi-layer: email → tunggu → kondisi → aksi. Berhenti otomatis saat prospek membalas.",
    card("Buat sequence", form([
      { name: "name", label: "Nama", required: true },
      { name: "from_email", label: "Dari (email di domain terverifikasi)", required: true },
      { name: "days", label: "Hari kirim", type: "select", options: [["1,2,3,4,5", "Senin–Jumat"], ["", "Setiap hari"]] },
      { name: "hours", label: "Jam kirim (WIB)", type: "select", options: [["8-17", "08.00–17.00"], ["0-0", "Kapan saja"]] },
      { name: "definition", label: "Definisi funnel (JSON)", type: "textarea", code: true, value: sequenceTemplate, full: true,
        help: "Node: email, wait, condition (opened/clicked/replied/has_tag/field_equals), action (add_tag/remove_tag/send_sms/webhook/notify/stop), end. Harus ada wait di antara dua email." },
    ], "Simpan", async (v) => {
      let def;
      try { def = JSON.parse(v.definition); } catch { throw new Error("JSON definisi tidak valid"); }
      const [sh, eh] = v.hours.split("-").map(Number);
      const s = await api("POST", "/v1/sequences", { name: v.name, from_email: v.from_email, definition: def,
        send_window: { days: v.days ? v.days.split(",").map(Number) : [], start_hour: sh, end_hour: eh, timezone: "Asia/Jakarta" } });
      toast("Sequence dibuat"); location.hash = "#/sequences/" + s.id;
    })),
    card("Semua sequence", table([
      { label: "Nama", render: (r) => h("a", { href: "#/sequences/" + r.id }, r.name) },
      { label: "Layer email", render: (r) => r.definition.nodes.filter((n) => n.type === "email").length },
      { label: "Status", render: (r) => badge(r.status) }, { label: "Dibuat", render: (r) => fmtDate(r.created_at) },
    ], list.data, "Belum ada sequence.")));
};

async function sequenceDetail(id) {
  const [s, rep] = await Promise.all([api("GET", "/v1/sequences/" + id), api("GET", "/v1/sequences/" + id + "/report")]);
  const steps = new Map(rep.steps.map((x) => [x.node_id + "/" + x.variant, x]));
  return page(s.name, "Dari " + s.from_email,
    h("div", { class: "row", style: "margin-bottom:16px" }, badge(s.status),
      s.status !== "active"
        ? h("button", { class: "primary", onclick: (e) => run(e.target, async () => { await api("POST", `/v1/sequences/${id}/activate`); toast("Sequence aktif"); route(); }) }, "Aktifkan")
        : h("button", { onclick: (e) => run(e.target, async () => { await api("POST", `/v1/sequences/${id}/pause`); toast("Sequence dijeda"); route(); }) }, "Jeda"),
      h("a", { href: "#/sequences" }, "← Semua sequence")),
    h("div", { class: "grid cols-2" },
      card("Daftarkan kontak", form([{ name: "tag", label: "Tag kontak", placeholder: "b2b", required: true, full: true }], "Daftarkan", async (v) => {
        const r = await api("POST", `/v1/sequences/${id}/enroll`, { tag: v.tag }); toast(r.enrolled + " kontak didaftarkan"); route();
      })),
      card("Status peserta", table([{ label: "Status", render: (r) => badge(r.k) }, { label: "Jumlah", key: "v" }],
        Object.entries(rep.enrollments).map(([k, v]) => ({ k, v })), "Belum ada peserta."),
        Object.keys(rep.stop_reasons).length ? h("p", { class: "help" }, "Alasan berhenti: " + Object.entries(rep.stop_reasons).map(([k, v]) => `${k} (${v})`).join(", ")) : null)),
    card("Alur funnel & performa per layer", table([
      { label: "Node", key: "id", mono: true },
      { label: "Tipe", render: (n) => nodeLabel[n.type] || n.type },
      { label: "Detail", render: describeNode },
      { label: "Menunggu di sini", render: (n) => rep.waiting_at[n.id] || 0 },
      { label: "Terkirim / balas", render: (n) => {
        if (n.type !== "email") return "—";
        const rows = [...steps.entries()].filter(([k]) => k.startsWith(n.id + "/")).map(([, v]) => v);
        if (!rows.length) return "0";
        return rows.map((r) => h("div", {}, `varian ${r.variant}: ${r.sent} terkirim · ${r.opened} buka · ${r.replied} balas (${pct(r.reply_rate)})`));
      } },
    ], s.definition.nodes)));
}

pages.inbox = async () => {
  const q = new URLSearchParams(location.hash.split("?")[1] || "");
  const cls = q.get("c") || "";
  const [items, rules] = await Promise.all([api("GET", "/v1/inbox?classification=" + encodeURIComponent(cls)), api("GET", "/v1/inbound/rules")]);
  const filters = [["", "Semua"], ["interested", "Tertarik"], ["question", "Bertanya"], ["not_interested", "Tidak tertarik"],
    ["out_of_office", "Sedang cuti"], ["wrong_person", "Salah orang"], ["unsubscribe", "Minta berhenti"], ["other", "Lainnya"]];
  return page("Inbox & otomasi", "Balasan dari semua alamat diklasifikasikan otomatis; aturan menangani auto-reply dan forward.",
    card(null, h("div", { class: "row", style: "margin-bottom:10px" }, filters.map(([f, label]) =>
      h("a", { href: "#/inbox?c=" + f, class: "badge " + (f === cls ? "info" : "") }, label))),
      table([
        { label: "Diterima", render: (r) => fmtDate(r.received_at) },
        { label: "Dari", render: (r) => [r.from_name, h("div", { class: "help" }, r.from)] },
        { label: "Ke", key: "rcpt" },
        { label: "Subjek & cuplikan", render: (r) => [r.subject, h("div", { class: "help" }, r.snippet)] },
        { label: "Klasifikasi", render: (r) => badge(r.classification) },
        { label: "Balasan kampanye?", render: (r) => r.related_message_id ? h("span", { class: "badge ok" }, "ya") : "—" },
      ], items.data, "Belum ada email masuk. Kirim email ke alamat di domain Anda yang terverifikasi.")),
    h("div", { class: "grid cols-2" },
      card("Tambah aturan", form([
        { name: "name", label: "Nama aturan", required: true, full: true },
        { name: "type", label: "Aksi", type: "select", options: [["auto_reply", "Balas otomatis"], ["forward", "Teruskan"]] },
        { name: "to", label: "Hanya untuk alamat (opsional)", placeholder: "info, sales@perusahaan.co.id" },
        { name: "classification", label: "Hanya klasifikasi (opsional)", placeholder: "interested" },
        { name: "forward_to", label: "Teruskan ke (untuk aksi Teruskan)", placeholder: "sales1@..., sales2@..." },
        { name: "body", label: "Isi balasan (untuk Balas otomatis)", type: "textarea", full: true,
          value: "Terima kasih {{from_name}}, pesan Anda \"{{subject}}\" sudah kami terima dan akan kami balas segera." },
      ], "Simpan aturan", async (v) => {
        const fwd = csv(v.forward_to);
        await api("POST", "/v1/inbound/rules", { name: v.name, enabled: true,
          match: { to: csv(v.to), classification: csv(v.classification) },
          action: v.type === "forward" ? { type: "forward", forward_to: fwd, round_robin: fwd.length > 1 } : { type: "auto_reply", body: v.body } });
        toast("Aturan disimpan"); route();
      })),
      card("Aturan aktif", table([
        { label: "Nama", key: "name" }, { label: "Aksi", render: (r) => r.action.type === "forward" ? "Teruskan → " + r.action.forward_to.join(", ") + (r.action.round_robin ? " (bergiliran)" : "") : "Balas otomatis" },
        { label: "", render: (r) => h("button", { class: "sm danger", onclick: (e) => run(e.target, async () => { await api("DELETE", "/v1/inbound/rules/" + r.id); route(); }) }, "Hapus") },
      ], rules.data, "Belum ada aturan."))));
};

pages.aliases = async () => {
  const list = await api("GET", "/v1/aliases");
  return page("Alias email unik", "Alamat khusus per sales, divisi, atau kampanye di domain Anda.",
    h("div", { class: "grid cols-2" },
      card("Buat satu alias", form([
        { name: "address", label: "Alamat", placeholder: "promo-nov@perusahaan.co.id", required: true, full: true },
        { name: "display_name", label: "Nama tampilan" }, { name: "forward_to", label: "Teruskan ke (opsional)" },
      ], "Buat", async (v) => { await api("POST", "/v1/aliases", { address: v.address, display_name: v.display_name, forward_to: csv(v.forward_to) }); toast("Alias dibuat"); route(); })),
      card("Generate massal dari pola", form([
        { name: "domain", label: "Domain", required: true }, { name: "pattern", label: "Pola", value: "{first}.{division}", required: true },
        { name: "rows", label: "Data (satu per baris: nama,divisi)", type: "textarea", full: true, value: "Rina,Sales\nAndi,Marketing" },
      ], "Generate", async (v) => {
        const rows = v.rows.split("\n").map((l) => l.split(",").map((x) => x.trim())).filter((r) => r[0]).map(([first, division]) => ({ first, division }));
        const r = await api("POST", "/v1/aliases/generate", { domain: v.domain, pattern: v.pattern, rows });
        toast((r.created || []).length + " alias dibuat"); route();
      }))),
    card("Semua alias", table([
      { label: "Alamat", key: "address" }, { label: "Nama", key: "display_name" },
      { label: "Diteruskan ke", render: (r) => r.forward_to.join(", ") || "—" },
      { label: "", render: (r) => h("button", { class: "sm danger", onclick: (e) => run(e.target, async () => { await api("DELETE", "/v1/aliases/" + r.id); route(); }) }, "Hapus") },
    ], list.data, "Belum ada alias. Domain harus terverifikasi dulu.")));
};

pages.otp = async () => {
  const out = h("div");
  let lastId = "";
  return page("OTP", "Kirim kode verifikasi dari email bisnis Anda sendiri, dengan fallback SMS opsional.",
    h("div", { class: "grid cols-2" },
      card("Kirim kode", form([
        { name: "channel", label: "Kanal", type: "select", options: [["email", "Email"], ["sms", "SMS"]] },
        { name: "to", label: "Penerima (email / no. HP)", required: true },
        { name: "from", label: "Dari (untuk email)", placeholder: "no-reply@perusahaan.co.id" },
        { name: "brand", label: "Nama brand", value: me.workspace.name },
      ], "Kirim OTP", async (v) => {
        const r = await api("POST", "/v1/otp/send", v);
        lastId = r.id;
        out.replaceChildren(h("div", { class: "callout" }, "Kode dikirim. ID: ", h("span", { class: "mono" }, r.id), " · berlaku sampai " + fmtDate(r.expires_at)));
      })),
      card("Verifikasi kode", form([{ name: "code", label: "Kode", required: true, full: true }], "Verifikasi", async (v) => {
        if (!lastId) throw new Error("Kirim kode dulu");
        const r = await api("POST", "/v1/otp/verify", { id: lastId, code: v.code });
        out.replaceChildren(h("div", { class: "callout " + (r.valid ? "" : "warn") }, r.valid ? "✅ Kode valid" : `❌ ${r.reason} (sisa percobaan: ${r.attempts_remaining})`));
      }))), out);
};

pages.sms = async () => {
  const list = await api("GET", "/v1/sms");
  return page("SMS", "Kirim lewat API, atau lewat SMTP ke <nomor>@sms.<domain>. Tiap segmen memakai 1 kredit.",
    card("Kirim SMS", form([
      { name: "to", label: "Nomor HP", placeholder: "0812xxxxxxx", required: true },
      { name: "sender_id", label: "Sender ID (opsional)" },
      { name: "body", label: "Pesan", type: "textarea", full: true, required: true },
    ], "Kirim", async (v) => { const r = await api("POST", "/v1/sms", v); toast(`SMS diantrikan (${r.segments} segmen, ${r.encoding})`); route(); })),
    card("Riwayat", table([
      { label: "Waktu", render: (r) => fmtDate(r.created_at) }, { label: "Ke", key: "to" }, { label: "Pesan", key: "body" },
      { label: "Sumber", key: "source" }, { label: "Provider", key: "provider" }, { label: "Status", render: (r) => badge(r.status) },
    ], list.data, "Belum ada SMS.")));
};

// flash keeps a one-time secret visible across the re-render that follows its creation.
let flash = null;

pages.settings = async () => {
  const [keys, hooks, supp] = await Promise.all([api("GET", "/v1/api-keys"), api("GET", "/v1/webhooks"), api("GET", "/v1/suppressions")]);
  const secretBox = h("div");
  if (flash) {
    secretBox.append(h("div", { class: "callout warn" }, h("b", {}, flash.title), h("div", { class: "secret", style: "margin:6px 0" }, flash.value), flash.note));
    flash = null;
  }
  const showSecret = (title, value, note) => { flash = { title, value, note }; route(); };
  return page("Pengaturan", "API key (juga password SMTP), webhook event, dan daftar blokir.",
    secretBox,
    h("div", { class: "grid cols-2" },
      card("API key", form([{ name: "name", label: "Nama key", placeholder: "aplikasi-saya", required: true, full: true }], "Buat key", async (v) => {
        const r = await api("POST", "/v1/api-keys", { name: v.name });
        showSecret("API key baru (simpan sekarang, tidak akan ditampilkan lagi):", r.key, "Pakai sebagai Bearer token, atau sebagai password SMTP (username bebas).");
      }), table([
        { label: "Nama", key: "name" }, { label: "Prefix", key: "prefix", mono: true },
        { label: "Status", render: (r) => r.revoked_at ? badge("revoked") : h("span", { class: "badge ok" }, "aktif") },
        { label: "", render: (r) => r.revoked_at ? "" : h("button", { class: "sm danger", onclick: (e) => run(e.target, async () => { await api("DELETE", "/v1/api-keys/" + r.id); route(); }) }, "Cabut") },
      ], keys.data, "Belum ada API key.")),
      card("Webhook", form([
        { name: "url", label: "URL endpoint", placeholder: "https://aplikasi-anda.com/webhooks/balasemail", required: true, full: true },
        { name: "events", label: "Event (kosong = semua)", placeholder: "bounce, complaint, reply", full: true },
      ], "Tambah webhook", async (v) => {
        const r = await api("POST", "/v1/webhooks", { url: v.url, events: csv(v.events) });
        showSecret("Secret webhook (simpan sekarang):", r.secret, "Verifikasi header X-BalasEmail-Signature di setiap request.");
      }), table([
        { label: "URL", key: "url", mono: true }, { label: "Event", render: (r) => r.events.join(", ") || "semua" },
        { label: "", render: (r) => h("button", { class: "sm danger", onclick: (e) => run(e.target, async () => { await api("DELETE", "/v1/webhooks/" + r.id); route(); }) }, "Hapus") },
      ], hooks.data, "Belum ada webhook."))),
    card("Daftar blokir (suppression)", form([{ name: "values", label: "Email atau domain (pisahkan koma)", full: true }], "Blokir", async (v) => {
      await api("POST", "/v1/suppressions", { values: csv(v.values) }); toast("Ditambahkan ke daftar blokir"); route();
    }), table([
      { label: "Nilai", key: "value" }, { label: "Jenis", key: "kind" }, { label: "Alasan", render: (r) => badge(r.reason) },
      { label: "Sejak", render: (r) => fmtDate(r.created_at) },
      { label: "", render: (r) => ["complaint", "erasure_request"].includes(r.reason) ? h("span", { class: "help" }, "permanen") :
        h("button", { class: "sm", onclick: (e) => run(e.target, async () => { await api("DELETE", "/v1/suppressions/" + encodeURIComponent(r.value)); route(); }) }, "Buka blokir") },
    ], supp.data, "Daftar blokir kosong.")));
};

// ---------- auth screen ----------
function authScreen() {
  let mode = "login";
  const body = h("div");
  const draw = () => {
    body.replaceChildren(
      h("div", { class: "tabs" },
        h("button", { class: mode === "login" ? "on" : "", onclick: () => { mode = "login"; draw(); } }, "Masuk"),
        h("button", { class: mode === "register" ? "on" : "", onclick: () => { mode = "register"; draw(); } }, "Daftar")),
      mode === "login"
        ? form([{ name: "email", label: "Email bisnis", type: "email", required: true }, { name: "password", label: "Password", type: "password", required: true }],
          "Masuk", async (v) => { const s = await api("POST", "/v1/auth/login", v); store.token = s.token; route(); })
        : form([
          { name: "company_name", label: "Nama perusahaan", required: true },
          { name: "full_name", label: "Nama lengkap", required: true },
          { name: "email", label: "Email bisnis", type: "email", required: true, help: "Gmail, Yahoo, Outlook, dan email sementara tidak diterima." },
          { name: "password", label: "Password (min. 10 karakter)", type: "password", required: true },
          { name: "postal_address", label: "Alamat kantor", help: "Ditampilkan di footer email marketing (wajib secara hukum)." },
        ], "Buat akun", async (v) => { const s = await api("POST", "/v1/auth/register", v); store.token = s.token; toast("Akun dibuat"); route(); }));
  };
  draw();
  return h("div", { class: "auth" }, h("div", { class: "card" },
    h("div", { class: "brand" }, h("div", { class: "logo" }, "✉"), "BalasEmail"),
    h("p", { class: "sub", style: "text-align:center;margin:0" }, "Outreach & otomasi email B2B"), body));
}

// ---------- shell & router ----------
const nav = [
  ["Utama", [["overview", "Ringkasan"]]],
  ["Prospek", [["finder", "Cari email"], ["validate", "Validasi"], ["contacts", "Kontak"]]],
  ["Kirim", [["campaigns", "Kampanye"], ["sequences", "Sequence"], ["otp", "OTP"], ["sms", "SMS"]]],
  ["Terima", [["inbox", "Inbox & aturan"], ["aliases", "Alias"]]],
  ["Akun", [["domains", "Domain"], ["settings", "Pengaturan"]]],
];

async function route() {
  if (!store.token) { $app.replaceChildren(authScreen()); return; }
  const [name, id] = (location.hash.replace(/^#\/?/, "").split("?")[0] || "overview").split("/");
  const current = pages[name] ? name : "overview";
  const main = h("main", {}, h("div", { class: "empty" }, "Memuat…"));
  $app.replaceChildren(h("div", { class: "shell" },
    h("aside", { class: "side" },
      h("div", { class: "brand" }, h("div", { class: "logo" }, "✉"), "BalasEmail"),
      h("nav", { class: "nav" }, nav.map(([group, items]) => [h("div", { class: "nav-group" }, group),
        items.map(([key, label]) => h("a", { href: "#/" + key, class: key === current ? "active" : "" }, label))]),
        h("div", { class: "nav-group" }, ""),
        h("a", { href: "#", onclick: (e) => { e.preventDefault(); store.token = ""; me = null; route(); } }, "Keluar"))),
    main));
  try {
    if (current === "overview") me = null; // refresh quota & credits
    me = me || await api("GET", "/v1/me");
    main.replaceChildren(...[await pages[current](id)].flat());
  } catch (e) {
    if (!store.token) return;
    main.replaceChildren(card("Terjadi kesalahan", h("p", {}, e.message)));
  }
}

window.addEventListener("hashchange", route);
route();
