// BalasEmail charts — small, dependency-free SVG/HTML charts that follow the
// dataviz rules: series colours come from validated CSS tokens (--s1..--s4),
// text never wears the data colour, thin marks with rounded data-ends, a 2px
// surface gap/ring, recessive hairline grid, hover tooltips, and a table view.
"use strict";

window.Charts = (() => {
  const NS = "http://www.w3.org/2000/svg";
  const svg = (tag, attrs = {}, style = {}) => {
    const el = document.createElementNS(NS, tag);
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    Object.assign(el.style, style); // CSSOM (allowed by our CSP) so colours can be CSS variables
    return el;
  };
  const div = (cls, text) => { const d = document.createElement("div"); if (cls) d.className = cls; if (text != null) d.textContent = text; return d; };
  const fmt = (n) => n >= 1e6 ? (n / 1e6).toFixed(1).replace(/\.0$/, "") + " jt" : n >= 1e3 ? (n / 1e3).toFixed(1).replace(/\.0$/, "") + " rb" : String(Math.round(n));
  const full = (n) => Number(n).toLocaleString("id-ID");
  const dayLabel = (iso) => new Date(iso + "T00:00:00").toLocaleDateString("id-ID", { day: "numeric", month: "short" });

  // niceTicks returns 0..max in 4-5 clean steps.
  function niceTicks(max) {
    if (max <= 0) return [0, 1];
    const raw = max / 4, mag = 10 ** Math.floor(Math.log10(raw));
    // counts are integers, so never step below 1 (avoids duplicate "1" labels)
    const step = Math.max(1, [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw));
    const ticks = [];
    for (let v = 0; v <= max + step * 0.001; v += step) ticks.push(v);
    if (ticks[ticks.length - 1] < max) ticks.push(ticks[ticks.length - 1] + step);
    return ticks;
  }

  // frame wraps a chart with a legend row, a "Tabel" toggle and a tooltip layer.
  function frame(opts, drawChart, rows) {
    const root = div("chart");
    const head = div("chart-head");
    if (opts.legend && opts.legend.length > 1) {
      const lg = div("legend");
      for (const s of opts.legend) {
        const item = div("legend-item");
        const sw = div("swatch"); sw.style.background = `var(${s.color})`;
        item.append(sw, document.createTextNode(s.name));
        lg.append(item);
      }
      head.append(lg);
    } else head.append(div("legend"));
    const toggle = document.createElement("button");
    toggle.type = "button"; toggle.className = "sm ghost"; toggle.textContent = "Tabel";
    head.append(toggle);
    const body = div("chart-body");
    const tableBox = div("chart-table"); tableBox.hidden = true;
    root.append(head, body, tableBox);

    // Table view: the accessible, exact-value alternative to the graphic.
    const t = document.createElement("table");
    const tr = document.createElement("tr");
    for (const c of rows.cols) { const th = document.createElement("th"); th.textContent = c; tr.append(th); }
    const thead = document.createElement("thead"); thead.append(tr);
    const tbody = document.createElement("tbody");
    for (const r of rows.data) {
      const row = document.createElement("tr");
      for (const v of r) { const td = document.createElement("td"); td.textContent = v; row.append(td); }
      tbody.append(row);
    }
    t.append(thead, tbody); tableBox.append(t);
    toggle.addEventListener("click", () => {
      const showTable = tableBox.hidden;
      tableBox.hidden = !showTable; body.hidden = showTable;
      toggle.textContent = showTable ? "Grafik" : "Tabel";
    });

    const draw = () => { if (!body.hidden && body.isConnected) { body.replaceChildren(); drawChart(body); } };
    root._draw = draw;
    return root;
  }

  function tooltip(host) {
    const tip = div("chart-tip"); tip.hidden = true; host.append(tip);
    return {
      show(x, y, title, lines) {
        tip.replaceChildren(div("tip-title", title));
        for (const l of lines) {
          const row = div("tip-row");
          if (l.color) { const sw = div("swatch"); sw.style.background = `var(${l.color})`; row.append(sw); }
          row.append(div("tip-name", l.name), div("tip-val", l.value));
          tip.append(row);
        }
        tip.hidden = false;
        const w = host.clientWidth, tw = tip.offsetWidth;
        tip.style.left = Math.min(Math.max(x + 14, 0), w - tw - 4) + "px";
        tip.style.top = Math.max(y - 10, 0) + "px";
      },
      hide() { tip.hidden = true; },
    };
  }

  // line: time series, one shared y-axis (never dual axis).
  function line({ labels, series, height = 260 }) {
    const rows = { cols: ["Tanggal", ...series.map((s) => s.name)], data: labels.map((d, i) => [dayLabel(d), ...series.map((s) => full(s.values[i]))]) };
    return frame({ legend: series }, (host) => {
      const W = host.clientWidth || 640, H = height, m = { t: 12, r: 16, b: 28, l: 40 };
      const iw = W - m.l - m.r, ih = H - m.t - m.b;
      const max = Math.max(1, ...series.flatMap((s) => s.values));
      const ticks = niceTicks(max), top = ticks[ticks.length - 1];
      const x = (i) => m.l + (labels.length === 1 ? iw / 2 : (i * iw) / (labels.length - 1));
      const y = (v) => m.t + ih - (v / top) * ih;
      const s = svg("svg", { width: W, height: H, role: "img", "aria-label": series.map((x) => x.name).join(", ") });
      for (const t of ticks) {
        s.append(svg("line", { x1: m.l, x2: W - m.r, y1: y(t), y2: y(t), class: "grid" }));
        const tx = svg("text", { x: m.l - 8, y: y(t) + 4, "text-anchor": "end", class: "axis" }); tx.textContent = fmt(t); s.append(tx);
      }
      const every = Math.ceil(labels.length / Math.max(2, Math.floor(iw / 70)));
      labels.forEach((d, i) => {
        if (i % every !== 0 && i !== labels.length - 1) return;
        const tx = svg("text", { x: x(i), y: H - 8, "text-anchor": i === 0 ? "start" : i === labels.length - 1 ? "end" : "middle", class: "axis" });
        tx.textContent = dayLabel(d); s.append(tx);
      });
      series.forEach((ser, k) => {
        if (k === 0) { // a light wash under the primary series only
          const area = `M${x(0)},${y(0)} ` + ser.values.map((v, i) => `L${x(i)},${y(v)}`).join(" ") + ` L${x(labels.length - 1)},${y(0)} Z`;
          s.append(svg("path", { d: area, class: "area" }, { fill: `var(${ser.color})` }));
        }
        s.append(svg("path", { d: ser.values.map((v, i) => `${i ? "L" : "M"}${x(i)},${y(v)}`).join(" "), class: "line" }, { stroke: `var(${ser.color})` }));
        const last = labels.length - 1;
        s.append(svg("circle", { cx: x(last), cy: y(ser.values[last]), r: 4, class: "dot" }, { fill: `var(${ser.color})` }));
      });
      const cross = svg("line", { y1: m.t, y2: m.t + ih, class: "cross" }); cross.style.display = "none"; s.append(cross);
      const hover = svg("g"); s.append(hover);
      const overlay = svg("rect", { x: m.l, y: m.t, width: iw, height: ih, fill: "transparent" }); s.append(overlay);
      host.append(s);
      const tip = tooltip(host);
      const move = (ev) => {
        const r = s.getBoundingClientRect();
        const px = ev.clientX - r.left;
        const i = Math.max(0, Math.min(labels.length - 1, Math.round(((px - m.l) / iw) * (labels.length - 1))));
        cross.setAttribute("x1", x(i)); cross.setAttribute("x2", x(i)); cross.style.display = "";
        hover.replaceChildren(...series.map((ser) => svg("circle", { cx: x(i), cy: y(ser.values[i]), r: 4, class: "dot" }, { fill: `var(${ser.color})` })));
        tip.show(x(i), m.t, dayLabel(labels[i]), series.map((ser) => ({ name: ser.name, value: full(ser.values[i]), color: ser.color })));
      };
      overlay.addEventListener("mousemove", move);
      overlay.addEventListener("mouseleave", () => { cross.style.display = "none"; hover.replaceChildren(); tip.hide(); });
    }, rows);
  }

  // columns: one series over days (vertical bars, ≤24px, 4px rounded top).
  function columns({ labels, values, name, color = "--s1", height = 220 }) {
    const rows = { cols: ["Tanggal", name], data: labels.map((d, i) => [dayLabel(d), full(values[i])]) };
    return frame({ legend: [] }, (host) => {
      const W = host.clientWidth || 640, H = height, m = { t: 12, r: 8, b: 28, l: 40 };
      const iw = W - m.l - m.r, ih = H - m.t - m.b;
      const ticks = niceTicks(Math.max(1, ...values)), top = ticks[ticks.length - 1];
      const band = iw / labels.length, bw = Math.min(24, band - 2);
      const y = (v) => m.t + ih - (v / top) * ih;
      const s = svg("svg", { width: W, height: H, role: "img", "aria-label": name });
      for (const t of ticks) {
        s.append(svg("line", { x1: m.l, x2: W - m.r, y1: y(t), y2: y(t), class: "grid" }));
        const tx = svg("text", { x: m.l - 8, y: y(t) + 4, "text-anchor": "end", class: "axis" }); tx.textContent = fmt(t); s.append(tx);
      }
      const tip = tooltip(host);
      const every = Math.ceil(labels.length / Math.max(2, Math.floor(iw / 70)));
      values.forEach((v, i) => {
        const cx = m.l + band * i + band / 2, h = Math.max(0, y(0) - y(v)), r = Math.min(4, h, bw / 2);
        const x0 = cx - bw / 2, y0 = y(v);
        const d = h <= 0 ? "" : `M${x0},${y(0)} V${y0 + r} Q${x0},${y0} ${x0 + r},${y0} H${x0 + bw - r} Q${x0 + bw},${y0} ${x0 + bw},${y0 + r} V${y(0)} Z`;
        if (d) s.append(svg("path", { d, class: "bar" }, { fill: `var(${color})` }));
        const hit = svg("rect", { x: m.l + band * i, y: m.t, width: band, height: ih, fill: "transparent" });
        hit.addEventListener("mousemove", () => tip.show(cx, Math.min(y0, y(0) - 8), dayLabel(labels[i]), [{ name, value: full(v), color }]));
        hit.addEventListener("mouseleave", () => tip.hide());
        s.append(hit);
        if (i % every === 0 || i === labels.length - 1) {
          const tx = svg("text", { x: cx, y: H - 8, "text-anchor": "middle", class: "axis" }); tx.textContent = dayLabel(labels[i]); s.append(tx);
        }
      });
      host.prepend(s);
    }, rows);
  }

  // hbars: categories by magnitude, single hue, value at the tip.
  function hbars({ items, color = "--s1", empty = "Belum ada data." }) {
    const rows = { cols: ["Kategori", "Jumlah"], data: items.map((it) => [it.label, full(it.value)]) };
    return frame({ legend: [] }, (host) => {
      if (!items.length) { host.append(div("empty", empty)); return; }
      const max = Math.max(1, ...items.map((i) => i.value));
      for (const it of items) {
        const row = div("hbar");
        const track = div("hbar-track");
        const bar = div("hbar-fill"); bar.style.width = Math.max(2, (it.value / max) * 100) + "%"; bar.style.background = `var(${color})`;
        bar.title = `${it.label}: ${full(it.value)}`;
        track.append(bar);
        row.append(div("hbar-label", it.label), track, div("hbar-value", full(it.value)));
        host.append(row);
      }
    }, rows);
  }

  // stack100: parts of a whole as one 100% bar with 2px surface gaps (status colours).
  function stack100({ items, empty = "Belum ada data." }) {
    const total = items.reduce((a, b) => a + b.value, 0);
    const rows = { cols: ["Status", "Jumlah", "Persen"], data: items.map((it) => [it.label, full(it.value), total ? Math.round((it.value / total) * 100) + "%" : "0%"]) };
    return frame({ legend: [] }, (host) => {
      if (!total) { host.append(div("empty", empty)); return; }
      const bar = div("stack-bar");
      const tip = tooltip(host);
      for (const it of items.filter((i) => i.value > 0)) {
        const seg = div("stack-seg"); seg.style.flex = String(it.value); seg.style.background = `var(${it.color})`;
        seg.addEventListener("mousemove", (ev) => {
          const r = host.getBoundingClientRect();
          tip.show(ev.clientX - r.left, 0, it.label, [{ name: "Jumlah", value: `${full(it.value)} (${Math.round((it.value / total) * 100)}%)`, color: it.color }]);
        });
        seg.addEventListener("mouseleave", () => tip.hide());
        bar.append(seg);
      }
      const legend = div("legend stack-legend");
      for (const it of items) {
        const item = div("legend-item");
        const sw = div("swatch"); sw.style.background = `var(${it.color})`;
        item.append(sw, document.createTextNode(`${it.label} · ${full(it.value)}`));
        legend.append(item);
      }
      host.append(bar, legend);
    }, rows);
  }

  // spark: tiny trend line for stat tiles (decorative; the tile shows the number).
  function spark(values, color = "--s1") {
    const W = 120, H = 34, max = Math.max(1, ...values);
    const x = (i) => (values.length === 1 ? W / 2 : (i * (W - 6)) / (values.length - 1) + 3);
    const y = (v) => H - 4 - (v / max) * (H - 8);
    const s = svg("svg", { width: W, height: H, viewBox: `0 0 ${W} ${H}`, class: "spark", "aria-hidden": "true" });
    s.append(svg("path", { d: values.map((v, i) => `${i ? "L" : "M"}${x(i)},${y(v)}`).join(" "), class: "line" }, { stroke: `var(${color})` }));
    s.append(svg("circle", { cx: x(values.length - 1), cy: y(values[values.length - 1]), r: 3.5, class: "dot" }, { fill: `var(${color})` }));
    return s;
  }

  // mount draws every chart inside root once it is attached, and on resize.
  function mount(root) {
    const charts = [...root.querySelectorAll(".chart")];
    charts.forEach((c) => c._draw && c._draw());
    return charts;
  }
  let resizeTimer;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => document.querySelectorAll(".chart").forEach((c) => c._draw && c._draw()), 150);
  });

  return { line, columns, hbars, stack100, spark, mount, fmt, full };
})();
