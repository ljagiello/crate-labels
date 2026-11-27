// crate-labels UI. Plain ES module: no build step, no dependencies, works offline.

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];
const enc = encodeURIComponent;

function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "dataset") Object.assign(el.dataset, v);
    else if (k === "text") el.textContent = v;
    else if (k === "value" || k === "checked" || k === "selected") el[k] = v;
    else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v);
    else if (v === true) el.setAttribute(k, "");
    else el.setAttribute(k, String(v));
  }
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

function debounce(fn, ms) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}

async function apiError(res) {
  let message = `${res.status} ${res.statusText}`;
  try {
    const body = await res.json();
    if (body.error) message = body.error;
    else if (Array.isArray(body.detail)) {
      message = body.detail.map((d) => `${(d.loc || []).slice(1).join(".")}: ${d.msg}`).join("; ");
    } else if (body.detail) message = String(body.detail);
  } catch (_) { /* not JSON */ }
  const err = new Error(message);
  err.status = res.status;
  return err;
}

async function api(path, { method = "GET", body } = {}) {
  const init = { method, headers: {} };
  // Every write is JSON, even without a body: the server refuses other content types
  // on POST/PUT/PATCH/DELETE (CSRF guard).
  if (method !== "GET" && method !== "HEAD") {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body === undefined ? {} : body);
  }
  const res = await fetch(path, init);
  if (!res.ok) throw await apiError(res);
  return res.json();
}

function toast(message, kind = "info") {
  const el = h("div", { class: `toast ${kind}`, role: kind === "error" ? "alert" : "status" }, message);
  $("#toasts").append(el);
  setTimeout(() => el.remove(), kind === "error" ? 6500 : 3200);
}

function relTime(ms) {
  const s = (Date.now() - ms) / 1000;
  if (s < 50) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

const isEditable = (el) =>
  el && (el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName));
const looksLikeRef = (t) => /^\s*\[?[rm]?\d{1,10}\]?\s*$/i.test(t) || /discogs\.com/i.test(t);
const thumbSrc = (url) => (url ? `/api/discogs/image?url=${enc(url)}` : "");
const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;

// A placeholder so the bench is never blank; shown faded until a record is loaded.
const SAMPLE = {
  artist: "Some Artist",
  title: "Record Title",
  label: "Label",
  catno: "CAT 001",
  year: "1994",
  format: '12"',
  styles: ["Hardcore"],
  tracks: [
    { position: "A1", title: "Opening Track", bpm: 150, key: "8A", included: true },
    { position: "B1", title: "Flip Side", bpm: 148, key: "5A", included: true },
  ],
  qr_url: "https://www.discogs.com/",
};

const S = {
  templates: [],
  models: [],
  stocks: [], // stocks the selected printer takes (all stocks when none is chosen)
  printers: [],
  defaultPrinter: null,
  printerId: "",
  template: "",
  stock: "",
  options: {}, // template id -> option values
  settings: {},
  discogs: { token_set: false },
  current: null, // { data, original, release, origin: {kind, id?} }
  left: "results",
  results: { title: "", rows: [], error: "" },
  collection: { folder: 0, page: 1, sort: "artist", folders: null, data: null, error: "" },
  crates: [],
  crate: null,
  selection: new Set(),
  jobs: new Map(),
  history: [],
  labelsPrinted: 0,
  zoom: "fit",
  preview: null,
};

const saveUi = debounce(() => {
  api("/api/settings", {
    method: "PUT",
    body: { values: { "ui.template": S.template, "ui.stock": S.stock, "ui.options": S.options, "ui.bpm_input": S.bpmInput || null, "ui.bpm_from_videos": S.bpmVideos } },
  }).catch(() => {});
}, 600);

// --- boot -------------------------------------------------------------------------------

async function boot() {
  bindGlobalKeys();
  bindLabelsPage();
  bindPrintersPage();
  bindSettingsPage();
  window.addEventListener("hashchange", route);
  window.addEventListener("resize", debounce(layoutStage, 80));
  const [settings, templates, models, health] = await Promise.all([
    api("/api/settings"),
    api("/api/templates").catch(() => ({ templates: [] })),
    api("/api/models"),
    api("/api/health").catch(() => null),
  ]);
  S.health = health;
  applySettings(settings);
  S.templates = templates.templates;
  S.models = models.models;
  S.template = S.settings["ui.template"] || S.settings.default_template || "";
  S.stock = S.settings["ui.stock"] || "";
  S.options = S.settings["ui.options"] || {};
  S.bpmInput = S.settings["ui.bpm_input"] || "";
  S.bpmVideos = S.settings["ui.bpm_from_videos"] !== false;
  await loadPrinters();
  await refreshStocks();
  renderDesignBar();
  renderEditor();
  renderLeft();
  route();
  schedulePreview();
  loadHistory();
  loadJobs();
  connectEvents();
}

function applySettings(settings) {
  S.settings = settings.values || {};
  S.discogs = settings.discogs || { token_set: false };
  S.labelsPrinted = settings.labels_printed || 0;
  S.dataDir = settings.data_dir;
  renderAccount();
}

function route() {
  const page = (location.hash.replace(/^#\/?/, "") || "labels").split("/")[0];
  const known = ["labels", "printers", "settings"].includes(page) ? page : "labels";
  for (const p of ["labels", "printers", "settings"]) $(`#page-${p}`).hidden = p !== known;
  for (const a of $$(".tabs a")) {
    if (a.dataset.page === known) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
  if (known === "printers") renderPrintersPage();
  if (known === "settings") renderSettingsPage();
  if (known === "labels") requestAnimationFrame(layoutStage);
}

function renderAccount() {
  const el = $("#account");
  el.replaceChildren(
    S.discogs.username
      ? h("span", {}, "Discogs ", h("strong", {}, S.discogs.username))
      : S.discogs.token_set
        ? h("span", {}, "Discogs token set")
        : h("a", { href: "#/settings" }, "Add your Discogs token"),
  );
}

// --- keyboard ------------------------------------------------------------------------

function bindGlobalKeys() {
  document.addEventListener("keydown", (e) => {
    const mod = e.metaKey || e.ctrlKey;
    if (mod && e.key.toLowerCase() === "p") {
      e.preventDefault();
      if ($("#page-labels").hidden) location.hash = "#/";
      printCurrent();
      return;
    }
    if (e.key === "/" && !isEditable(e.target) && !mod) {
      e.preventDefault();
      location.hash = "#/";
      $("#q").focus();
      $("#q").select();
      return;
    }
    if (e.key === "Escape" && e.target === $("#q")) $("#q").blur();
  });
  document.addEventListener("paste", (e) => {
    if (isEditable(e.target)) return;
    const text = e.clipboardData?.getData("text") || "";
    if (!text.trim()) return;
    e.preventDefault();
    location.hash = "#/";
    $("#q").value = text.trim();
    submitFind();
  });
}

function bindListKeys(container) {
  container.addEventListener("keydown", (e) => {
    if (!["ArrowDown", "ArrowUp"].includes(e.key)) return;
    const rows = $$(".row", container);
    const i = rows.indexOf(document.activeElement);
    if (i < 0) return;
    e.preventDefault();
    rows[Math.max(0, Math.min(rows.length - 1, i + (e.key === "ArrowDown" ? 1 : -1)))].focus();
  });
}

// --- labels page: find --------------------------------------------------------------------

function bindLabelsPage() {
  $("#find").addEventListener("submit", (e) => { e.preventDefault(); submitFind(); });
  $("#q").addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") {
      const first = $("#left-body .row");
      if (first) { e.preventDefault(); first.focus(); }
    }
  });
  for (const tab of $$(".segmented button")) {
    tab.addEventListener("click", () => { S.left = tab.dataset.left; renderLeft(); });
  }
  bindListKeys($("#left-body"));
  $("#template").addEventListener("change", (e) => {
    S.template = e.target.value;
    S.templatePinned = true; // a hand-picked template stays until the next record
    renderDesignBar();
    renderFitNote();
    saveUi();
    schedulePreview();
  });
  $("#stock").addEventListener("change", (e) => {
    S.stock = e.target.value;
    saveUi();
    schedulePreview();
  });
  $("#printer").addEventListener("change", async (e) => {
    S.printerId = e.target.value;
    await refreshStocks();
    renderDesignBar();
    fitTemplateToTracks();
    renderPrintNote();
    schedulePreview();
  });
  $("#print").addEventListener("click", printCurrent);
  for (const b of $$(".bench-zoom button")) {
    b.addEventListener("click", () => {
      S.zoom = b.dataset.zoom;
      for (const o of $$(".bench-zoom button")) o.setAttribute("aria-pressed", String(o === b));
      layoutStage();
    });
  }
}

async function submitFind() {
  const text = $("#q").value.trim();
  if (!text) return;
  const by = $("#find-by").value;
  if (by === "q" && looksLikeRef(text)) {
    await loadRef(text);
    return;
  }
  await search(text, by);
}

async function loadRef(text, { quiet = false } = {}) {
  setBenchStatus("Looking it up on Discogs…");
  try {
    const result = await api(`/api/discogs/lookup?q=${enc(text)}`);
    setCurrent(result.data, result.release, { kind: "discogs" });
    const r = result.release;
    if (!S.results.rows.some((row) => row.id === r.id)) {
      S.results.rows.unshift({ ...r, format: r.format });
      if (!S.results.title) S.results.title = "Looked up";
    }
    if (S.left === "results") renderLeft();
    if (result.ref.kind === "master" && !quiet) {
      toast(`Master ${result.ref.id}: showing its main pressing, release ${r.id}`);
    }
  } catch (err) {
    setBenchStatus(err.message, true);
    if (!quiet) toast(err.message, "error");
  }
}

async function search(text, by) {
  S.left = "results";
  S.results = { title: `Searching for “${text}”…`, rows: [], error: "" };
  renderLeft();
  const params = new URLSearchParams({ format: $("#find-vinyl").checked ? "Vinyl" : "" });
  params.set(by, text);
  try {
    const data = await api(`/api/discogs/search?${params}`);
    const n = data.pagination?.items ?? data.results.length;
    S.results = { title: `${plural(n, "match")} for “${text}”`, rows: data.results, error: "" };
  } catch (err) {
    S.results = { title: "", rows: [], error: err.message, needsToken: err.status === 401 };
  }
  renderLeft();
}

// --- labels page: left lists ---------------------------------------------------------------

function renderLeft() {
  for (const tab of $$(".segmented button")) {
    tab.setAttribute("aria-selected", String(tab.dataset.left === S.left));
  }
  const body = $("#left-body");
  if (S.left === "results") body.replaceChildren(...resultsView());
  else if (S.left === "collection") { body.replaceChildren(...collectionView()); loadCollection(); }
  else body.replaceChildren(...(S.crate ? crateView() : cratesView()));
}

function emptyState(title, text, action) {
  return h("div", { class: "list-empty" }, h("strong", {}, title), text, action ? h("div", { style: "margin-top:10px" }, action) : null);
}

function releaseRow(r, onOpen) {
  const meta = [r.label, r.catno].filter(Boolean).join(" ");
  const facts = [r.year, r.country, r.format].filter(Boolean).join(", ");
  const current = S.current?.data?.release_id === r.id;
  return h("button", {
    class: "row", type: "button", "aria-current": current ? "true" : "false",
    onclick: onOpen || (() => loadRef(String(r.id))),
  },
  r.thumb ? h("img", { class: "thumb", src: thumbSrc(r.thumb), alt: "", loading: "lazy" }) : h("span", { class: "thumb" }),
  h("span", { style: "min-width:0" },
    h("span", { class: "t1", style: "display:block" }, r.title || "Untitled"),
    h("span", { class: "t2", style: "display:block" }, r.artist || "—"),
    h("span", { class: "t3", style: "display:block" }, [meta, facts].filter(Boolean).join(" — ")),
  ),
  h("span", { class: "side" }, h("span", { class: "printed-dot", "data-on": String(Boolean(r.printed)), title: r.printed ? "Has a printed label" : "No label printed yet" })));
}

function resultsView() {
  const r = S.results;
  if (r.error) {
    return [emptyState(r.needsToken ? "Discogs search needs your token" : "Search failed", r.error,
      r.needsToken ? h("a", { class: "btn small", href: "#/settings" }, "Add token in Settings") : null)];
  }
  if (!r.rows.length) {
    return [emptyState(r.title || "Find a record",
      r.title ? "" : "Paste a release or master link, type a release id, or search by catalogue number, barcode, label or artist.")];
  }
  return [h("div", { class: "list-head hint" }, r.title), ...r.rows.map((row) => releaseRow(row))];
}

function collectionView() {
  if (!S.discogs.token_set) {
    return [emptyState("Your collection lives on Discogs",
      "Add your personal access token to browse folders and print labels for what you own.",
      h("a", { class: "btn small", href: "#/settings" }, "Add token in Settings"))];
  }
  const c = S.collection;
  const folderSel = h("select", {
    "aria-label": "Folder",
    onchange: (e) => { c.folder = Number(e.target.value); c.page = 1; c.data = null; renderLeft(); },
  }, (c.folders || [{ id: 0, name: "All", count: "" }]).map((f) =>
    h("option", { value: f.id, selected: f.id === c.folder }, `${f.name}${f.count !== "" ? ` (${f.count})` : ""}`)));
  const sortSel = h("select", {
    "aria-label": "Sort",
    onchange: (e) => { c.sort = e.target.value; c.page = 1; c.data = null; renderLeft(); },
  }, [["artist", "Artist"], ["label", "Label"], ["catno", "Cat. no."], ["year", "Year"], ["added", "Recently added"]]
    .map(([v, t]) => h("option", { value: v, selected: v === c.sort }, t)));
  const head = h("div", { class: "list-head" }, folderSel, sortSel);
  if (c.error) return [head, emptyState("Could not load your collection", c.error)];
  if (!c.data) return [head, h("p", { class: "list-empty" }, "Loading…")];
  const pg = c.data.pagination || {};
  const pager = h("div", { class: "pager" },
    h("button", { class: "btn small", type: "button", disabled: (pg.page || 1) <= 1, onclick: () => { c.page -= 1; c.data = null; renderLeft(); } }, "Previous"),
    h("span", { class: "hint" }, `Page ${pg.page || 1} of ${pg.pages || 1}`),
    h("button", { class: "btn small", type: "button", disabled: (pg.page || 1) >= (pg.pages || 1), onclick: () => { c.page += 1; c.data = null; renderLeft(); } }, "Next"));
  return [head, ...c.data.items.map((row) => releaseRow(row)), pager];
}

async function loadCollection() {
  const c = S.collection;
  if (!S.discogs.token_set || c.data || c.loading) return;
  c.loading = true;
  try {
    if (!c.folders) c.folders = (await api("/api/discogs/collection/folders")).folders;
    const order = c.sort === "added" ? "desc" : "asc";
    c.data = await api(`/api/discogs/collection?folder_id=${c.folder}&page=${c.page}&sort=${c.sort}&sort_order=${order}`);
    c.error = "";
  } catch (err) {
    c.error = err.message;
  } finally {
    c.loading = false;
  }
  if (S.left === "collection") $("#left-body").replaceChildren(...collectionView());
}

async function loadCrates() {
  S.crates = (await api("/api/crates")).crates;
}

function cratesView() {
  if (!S.cratesLoaded) {
    S.cratesLoaded = true;
    loadCrates().then(() => {
      renderEditorHead();
      if (S.left === "crates" && !S.crate) renderLeft();
    }).catch((e) => toast(e.message, "error"));
  }
  const name = h("input", { type: "text", placeholder: "Name a new crate", maxlength: 40, "aria-label": "Name for a new crate" });
  const form = h("form", {
    class: "list-head",
    onsubmit: async (e) => {
      e.preventDefault();
      try {
        // A blank name is fine: the server picks "Untitled crate", "Untitled crate 2", ...
        const crate = await api("/api/crates", { method: "POST", body: { name: name.value } });
        await loadCrates();
        openCrate(crate.id);
      } catch (err) { toast(err.message, "error"); }
    },
  }, name, h("button", { class: "btn small", type: "submit" }, "Create"));
  const rows = S.crates.map((c) => h("button", { class: "row", type: "button", style: "grid-template-columns:1fr auto", onclick: () => openCrate(c.id) },
    h("span", { class: "t1" }, c.name),
    h("span", { class: "hint" }, plural(c.record_count, "record"))));
  return [form, ...(rows.length ? rows : [emptyState("Make your first crate",
    "Group records you want labels for, put them in any order, then print the lot in one go.")])];
}

async function openCrate(id) {
  try {
    S.crate = await api(`/api/crates/${enc(id)}`);
    S.selection = new Set(S.crate.records.map((r) => r.id));
    S.left = "crates";
    renderLeft();
  } catch (err) { toast(err.message, "error"); }
}

function crateView() {
  const c = S.crate;
  const all = h("input", {
    type: "checkbox", "aria-label": "Select all", checked: S.selection.size === c.records.length && c.records.length > 0,
    onchange: (e) => { S.selection = e.target.checked ? new Set(c.records.map((r) => r.id)) : new Set(); renderLeft(); },
  });
  // The ids match service.CrateOrders.
  const order = h("select", { "aria-label": "Print order", id: "crate-order", title: "The order the labels come out in" },
    [["crate", "As arranged"], ["artist", "By artist"], ["catno", "By catalogue number"], ["newest", "Newest first"]]
      .map(([v, t]) => h("option", { value: v, selected: v === (S.crateOrder || "crate") }, t)));
  order.addEventListener("change", (e) => { S.crateOrder = e.target.value; });
  const head = h("div", { class: "list-head" },
    h("button", { class: "btn small quiet", type: "button", onclick: () => { S.crate = null; S.cratesLoaded = false; renderLeft(); } }, "All crates"),
    h("strong", { style: "flex:1" }, c.name),
    h("button", { class: "btn small quiet danger", type: "button", onclick: () => deleteCrate(c) }, "Delete"));
  const actions = h("div", { class: "list-head" }, all, order,
    h("button", { class: "btn small primary", type: "button", disabled: !S.selection.size, onclick: printCrate },
      `Print ${plural(S.selection.size, "label")}`));
  if (!c.records.length) {
    return [head, emptyState("Nothing in this crate", "Open a record, then pick this crate from “Add to crate…” next to its title.")];
  }
  const rows = c.records.map((item) => {
    const box = h("input", {
      type: "checkbox", "aria-label": `Include ${item.title}`, checked: S.selection.has(item.id),
      onclick: (e) => e.stopPropagation(),
      onchange: (e) => { e.target.checked ? S.selection.add(item.id) : S.selection.delete(item.id); refreshCrateCount(); },
    });
    const row = h("div", {
      class: "row crate-item", tabindex: "0", draggable: "true", role: "button",
      "aria-current": S.current?.origin?.recordId === item.id ? "true" : "false",
      onclick: () => openCrateRecord(item),
      onkeydown: (e) => { if (e.key === "Enter") openCrateRecord(item); },
    }, box,
    item.thumb ? h("img", { class: "thumb", src: thumbSrc(item.thumb), alt: "", loading: "lazy" }) : h("span", { class: "thumb" }),
    h("span", { style: "min-width:0" },
      h("span", { class: "t1", style: "display:block" }, item.title),
      h("span", { class: "t2", style: "display:block" }, item.artist),
      h("span", { class: "t3", style: "display:block" }, [item.catno, item.year, item.data ? "edited" : ""].filter(Boolean).join(", "))),
    h("span", { class: "side" },
      h("span", { class: "printed-dot", "data-on": String(item.printed), title: item.printed ? "Has a printed label" : "No label yet" }),
      h("button", { class: "btn small quiet", type: "button", title: "Take it out of this crate", "aria-label": `Take ${item.title} out`, onclick: (e) => { e.stopPropagation(); removeFromCrate(item); } }, "×")));
    row.addEventListener("dragstart", (e) => { S.dragId = item.id; row.classList.add("dragging"); e.dataTransfer.effectAllowed = "move"; });
    row.addEventListener("dragend", () => row.classList.remove("dragging"));
    row.addEventListener("dragover", (e) => { e.preventDefault(); row.classList.add("drop-before"); });
    row.addEventListener("dragleave", () => row.classList.remove("drop-before"));
    row.addEventListener("drop", (e) => { e.preventDefault(); row.classList.remove("drop-before"); moveCrateRecord(S.dragId, item.id); });
    return row;
  });
  return [head, actions, ...rows];
}

function refreshCrateCount() {
  const btn = $("#left-body .list-head .btn.primary");
  if (btn) { btn.textContent = `Print ${plural(S.selection.size, "label")}`; btn.disabled = !S.selection.size; }
}

async function moveCrateRecord(dragId, beforeId) {
  if (!dragId || dragId === beforeId) return;
  const ids = S.crate.records.map((r) => r.id).filter((id) => id !== dragId);
  ids.splice(ids.indexOf(beforeId), 0, dragId);
  try {
    S.crate = await api(`/api/crates/${enc(S.crate.id)}/order`, { method: "PUT", body: { record_ids: ids } });
    renderLeft();
  } catch (err) { toast(err.message, "error"); }
}

async function removeFromCrate(item) {
  try {
    S.crate = await api(`/api/crates/${enc(S.crate.id)}/records/${item.id}`, { method: "DELETE" });
    S.selection.delete(item.id);
    renderLeft();
  } catch (err) { toast(err.message, "error"); }
}

async function deleteCrate(c) {
  if (!confirm(`Delete the crate “${c.name}”? The records stay on Discogs; only this list goes.`)) return;
  try {
    await api(`/api/crates/${enc(c.id)}`, { method: "DELETE" });
    S.crate = null;
    S.cratesLoaded = false;
    renderLeft();
  } catch (err) { toast(err.message, "error"); }
}

async function openCrateRecord(item) {
  if (item.data) {
    setCurrent(item.data, null, { kind: "crate", crateId: S.crate.id, recordId: item.id, releaseId: item.release_id });
    loadReleaseSummary(item.release_id);
  } else {
    try {
      const result = await api(`/api/discogs/release/${item.release_id}`);
      setCurrent(result.data, result.release, { kind: "crate", crateId: S.crate.id, recordId: item.id });
    } catch (err) { toast(err.message, "error"); }
  }
  renderLeft();
}

async function loadReleaseSummary(id) {
  try {
    const result = await api(`/api/discogs/release/${id}`);
    if (S.current && S.current.data.release_id === id) {
      S.current.release = result.release;
      S.current.original = result.data;
      renderEditorHead();
      $("#tracks")?.replaceWith(tracksEditor());
      autoTempoFromVideos();
    }
  } catch (_) { /* offline: the edited data is enough */ }
}

async function printCrate() {
  if (!requirePrinter()) return;
  try {
    const job = await api(`/api/crates/${enc(S.crate.id)}/print`, {
      method: "POST",
      body: {
        template: S.template, stock: S.stock, printer_id: S.printerId,
        record_ids: S.crate.records.filter((r) => S.selection.has(r.id)).map((r) => r.id),
        order: S.crateOrder || "crate", copies: copies(), options: currentOptions(),
      },
    });
    upsertJob(job);
    toast(`Queued ${plural(job.total, "label")} from ${S.crate.name} on ${job.printer_name}`);
  } catch (err) { toast(err.message, "error"); }
}

// --- labels page: template, stock, options -------------------------------------------------

async function loadPrinters() {
  const data = await api("/api/printers");
  S.printers = data.printers;
  S.defaultPrinter = data.default;
  S.calibrationKeys = data.calibration_keys;
  if (!S.printers.some((p) => p.id === S.printerId)) {
    S.printerId = S.printers.some((p) => p.id === S.defaultPrinter) ? S.defaultPrinter : (S.printers[0]?.id || "");
  }
  const sel = $("#printer");
  sel.replaceChildren(
    ...(S.printers.length ? [] : [h("option", { value: "" }, "No printer yet (preview only)")]),
    ...S.printers.map((p) => h("option", { value: p.id, selected: p.id === S.printerId }, `${p.name} (${p.model})`)),
  );
  renderPrintNote();
}

async function refreshStocks() {
  const q = S.printerId ? `?printer_id=${enc(S.printerId)}` : "";
  S.stocks = (await api(`/api/stocks${q}`)).stocks;
}

function usableTemplates() {
  const ids = new Set(S.stocks.map((s) => s.id));
  return S.templates.filter((t) => t.stocks.some((id) => ids.has(id)));
}

function stocksForTemplate(t) {
  const allowed = new Set(t ? t.stocks : []);
  return S.stocks.filter((s) => allowed.has(s.id));
}

const currentTemplate = () => S.templates.find((t) => t.id === S.template);
const currentStock = () => S.stocks.find((s) => s.id === S.stock);

const tickedTracks = () => (S.current?.data.tracks || []).filter((t) => t.included !== false).length;

// fitTemplateToTracks switches between the track-based templates (DJ labels) so the label
// holds every ticked track: the smallest that fits, else the biggest this printer takes.
// It leaves seller tags, centre labels and the like alone, and a template the user picked
// by hand for this record.
function fitTemplateToTracks() {
  const cur = currentTemplate();
  const n = tickedTracks();
  if (!S.current || S.templatePinned || !cur?.max_tracks || !n) return;
  const pool = usableTemplates().filter((t) => t.max_tracks > 0);
  const fitting = pool.filter((t) => t.max_tracks >= n);
  const want = fitting.length ? Math.min(...fitting.map((t) => t.max_tracks)) : Math.max(...pool.map((t) => t.max_tracks));
  const pick = cur.max_tracks === want ? cur : pool.find((t) => t.max_tracks === want);
  if (!pick || pick.id === S.template) return;
  S.template = pick.id;
  renderDesignBar();
  saveUi();
}

function fitNoteText() {
  const tpl = currentTemplate();
  const n = tickedTracks();
  return tpl?.max_tracks && n > tpl.max_tracks
    ? `${n} tracks ticked; ${tpl.name} prints the first ${tpl.max_tracks}. Untick the ones to leave off.`
    : "";
}

function renderFitNote() {
  const note = $("#fit-note");
  if (note) note.textContent = fitNoteText();
}

function renderDesignBar() {
  const usable = usableTemplates();
  if (!usable.some((t) => t.id === S.template)) S.template = usable[0]?.id || "";
  $("#template").replaceChildren(
    ...(usable.length ? usable : [{ id: "", name: S.templates.length ? "No template fits this printer" : "No templates available" }])
      .map((t) => h("option", { value: t.id, selected: t.id === S.template, title: t.description || "" }, t.name)),
  );
  const tpl = currentTemplate();
  const stocks = stocksForTemplate(tpl);
  if (!stocks.some((s) => s.id === S.stock)) S.stock = stocks[0]?.id || "";
  $("#stock").replaceChildren(...stocks.map((s) => h("option", { value: s.id, selected: s.id === S.stock }, s.name)));
  renderOptions(tpl);
}

function currentOptions() {
  return { ...(S.options[S.template] || {}) };
}

function setOption(id, value) {
  S.options[S.template] = { ...(S.options[S.template] || {}), [id]: value };
  saveUi();
  schedulePreview();
}

function renderOptions(tpl) {
  const box = $("#options");
  if (!tpl) { box.replaceChildren(); return; }
  const values = currentOptions();
  box.replaceChildren(...(tpl.options || []).map((o) => optionControl(o, values[o.id] ?? o.default)));
}

function optionControl(o, value) {
  const label = o.label || o.id;
  if (o.type === "bool") {
    return h("label", { class: "field inline", title: o.help || "" },
      h("input", { type: "checkbox", checked: Boolean(value), onchange: (e) => setOption(o.id, e.target.checked) }),
      h("span", {}, label));
  }
  if (o.type === "choice") {
    const labels = o.choice_labels && o.choice_labels.length === o.choices.length ? o.choice_labels : o.choices;
    return h("label", { class: "field", title: o.help || "" }, h("span", {}, label),
      h("select", { onchange: (e) => setOption(o.id, e.target.value) },
        o.choices.map((c, i) => h("option", { value: c, selected: String(value) === String(c) }, labels[i]))));
  }
  if (o.type === "text" && o.id === "track") {
    const tracks = (S.current?.data || SAMPLE).tracks || [];
    return h("label", { class: "field", title: o.help || "" }, h("span", {}, label),
      h("select", { onchange: (e) => setOption(o.id, e.target.value) },
        h("option", { value: "", selected: !value }, "First ticked track"),
        tracks.map((t) => h("option", { value: t.position || t.title, selected: value && value === (t.position || t.title) },
          `${t.position ? `${t.position} ` : ""}${t.title}`))));
  }
  if (o.type === "int" || o.type === "float") {
    return h("label", { class: "field", style: "max-width:110px", title: o.help || "" }, h("span", {}, label),
      h("input", {
        type: "number", value: value ?? "", min: o.min ?? undefined, max: o.max ?? undefined,
        // Decimal options (today only the text size) move in tenths; the server clamps.
        step: o.type === "float" ? "0.1" : "1",
        oninput: debounce((e) => e.target.value !== "" && setOption(o.id, Number(e.target.value)), 250),
      }));
  }
  return h("label", { class: "field", title: o.help || "" }, h("span", {}, label),
    h("input", { type: "text", value: value ?? "", oninput: debounce((e) => setOption(o.id, e.target.value), 250) }));
}

// --- labels page: preview ------------------------------------------------------------------

let previewCtl = null;
let previewUrl = null;
const schedulePreview = debounce(runPreview, 90);

function setBenchStatus(text, error = false) {
  const el = $("#bench-status");
  el.textContent = text;
  el.classList.toggle("error", error);
}

async function runPreview() {
  const stage = $("#stage");
  $("#bench-empty").hidden = Boolean(S.current);
  stage.classList.toggle("sample", !S.current);
  if (!S.template || !S.stock) {
    setBenchStatus(S.templates.length ? "No template fits the stocks this printer takes." : "Label templates are not available in this build.", true);
    return;
  }
  previewCtl?.abort();
  previewCtl = new AbortController();
  stage.classList.add("busy");
  const body = {
    data: S.current ? S.current.data : SAMPLE,
    template: S.template,
    stock: S.stock,
    printer_id: S.printerId || null,
    options: currentOptions(),
  };
  try {
    const res = await fetch("/api/preview", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body), signal: previewCtl.signal,
    });
    if (!res.ok) throw await apiError(res);
    const blob = await res.blob();
    const mm = (res.headers.get("X-Design-Mm") || "0x0").split("x").map(Number);
    S.preview = {
      w: Number(res.headers.get("X-Canvas-Width")), h: Number(res.headers.get("X-Canvas-Height")),
      dpi: Number(res.headers.get("X-Canvas-Dpi")), mm, model: res.headers.get("X-Model-Id"),
    };
    if (previewUrl) URL.revokeObjectURL(previewUrl);
    previewUrl = URL.createObjectURL(blob);
    const img = $("#preview");
    img.src = previewUrl;
    await img.decode().catch(() => {});
    stage.classList.remove("busy");
    layoutStage();
    const model = S.models.find((m) => m.id === S.preview.model);
    setBenchStatus(`${S.preview.w} × ${S.preview.h} dots at ${Math.round(S.preview.dpi)} dpi${model ? `, as ${model.label} prints it` : ""}`);
  } catch (err) {
    if (err.name === "AbortError") return;
    stage.classList.remove("busy");
    setBenchStatus(err.message, true);
  }
}

function layoutStage() {
  const p = S.preview;
  const img = $("#preview");
  const bench = $("#bench");
  if (!p || !p.w || $("#page-labels").hidden) return;
  const availW = Math.max(120, bench.clientWidth - 140);
  const availH = Math.max(160, Math.min(window.innerHeight * 0.46, 560));
  let scale = 1;
  if (S.zoom === "fit") {
    const s = Math.min(availW / p.w, availH / p.h);
    scale = s >= 1 ? Math.floor(s) : s;
  }
  const W = Math.round(p.w * scale);
  const H = Math.round(p.h * scale);
  img.style.width = `${W}px`;
  img.style.height = `${H}px`;
  img.style.imageRendering = scale >= 1 ? "pixelated" : "auto";
  const stock = currentStock();
  $("#stage").classList.toggle("round", Boolean(stock?.round));
  drawMarks(W, H, p.mm);
  // Cutting-mat grid in true millimetres, aligned to the label's top-left corner.
  const pxPerMm = p.mm[0] ? W / p.mm[0] : 8;
  const minorMm = pxPerMm >= 4 ? 1 : 5;
  const minor = pxPerMm * minorMm;
  const major = pxPerMm * 10;
  const b = bench.getBoundingClientRect();
  const r = img.getBoundingClientRect();
  bench.style.setProperty("--grid-minor", `${minor}px`);
  bench.style.setProperty("--grid-major", `${major}px`);
  bench.style.setProperty("--grid-x", `${(r.left - b.left) % major}px`);
  bench.style.setProperty("--grid-y", `${(r.top - b.top) % major}px`);
}

function drawMarks(W, H, mm) {
  const svg = $("#marks");
  const M = 30;
  const ns = "http://www.w3.org/2000/svg";
  svg.setAttribute("width", W + 2 * M);
  svg.setAttribute("height", H + 2 * M);
  svg.style.left = `${-M}px`;
  svg.style.top = `${-M}px`;
  const parts = [];
  const line = (x1, y1, x2, y2) => {
    const el = document.createElementNS(ns, "line");
    for (const [k, v] of Object.entries({ x1, y1, x2, y2 })) el.setAttribute(k, v);
    parts.push(el);
  };
  const L = M, T = M, R = M + W, B = M + H;
  // crop ticks, outside each corner (as on a sheet of labels)
  for (const [x, y, dx, dy] of [[L, T, -1, -1], [R, T, 1, -1], [L, B, -1, 1], [R, B, 1, 1]]) {
    line(x + dx * 5, y, x + dx * 18, y);
    line(x, y + dy * 5, x, y + dy * 18);
  }
  const fmt = (v) => (Number.isInteger(v) ? v : v.toFixed(1));
  const top = document.createElementNS(ns, "text");
  top.setAttribute("x", L + W / 2);
  top.setAttribute("y", T - 10);
  top.setAttribute("text-anchor", "middle");
  top.textContent = `${fmt(mm[0])} mm`;
  const side = document.createElementNS(ns, "text");
  side.setAttribute("x", R + 12);
  side.setAttribute("y", T + H / 2);
  side.setAttribute("text-anchor", "middle");
  side.setAttribute("transform", `rotate(90 ${R + 12} ${T + H / 2})`);
  side.textContent = `${fmt(mm[1])} mm`;
  svg.replaceChildren(...parts, top, side);
}

// --- labels page: editor --------------------------------------------------------------------

function blankData() {
  return { artist: "", title: "", label: "", catno: "", year: "", country: "", format: '12"', genres: [], styles: [], tracks: [{ position: "A1", title: "", artist: "", duration: "", bpm: null, key: null, included: true }], notes: "", price: "", condition: "", qr_url: "", release_id: null };
}

function setCurrent(data, release, origin) {
  // The "which track" pick belongs to one record; a new record starts from its first track.
  for (const values of Object.values(S.options)) if (values && "track" in values) delete values.track;
  S.current = { data: structuredClone(data), original: structuredClone(data), release, origin, tempo: new WeakMap() };
  S.templatePinned = origin.kind === "history";
  fitTemplateToTracks();
  renderEditor();
  renderOptions(currentTemplate());
  schedulePreview();
  autoTempoFromVideos();
  if (S.left === "results") renderLeft();
}

function isDirty() {
  return S.current && JSON.stringify(S.current.data) !== JSON.stringify(S.current.original);
}

function edit(mutator) {
  mutator(S.current.data);
  schedulePreview();
  renderEditorHead();
}

function renderEditorHead() {
  const head = $("#editor-head");
  if (!head || !S.current) return;
  const d = S.current.data;
  const r = S.current.release;
  const meta = r
    ? [[r.label, r.catno].filter(Boolean).join(" "), r.year, r.country, r.format].filter(Boolean).join(", ")
    : S.current.origin.kind === "manual" ? "Typed by hand" : "";
  const crateChoices = () => [
    h("option", { value: "" }, "Add to crate…"),
    ...S.crates.map((c) => h("option", { value: c.id }, c.name)),
    h("option", { value: "+new" }, "New crate…"),
  ];
  const addSel = h("select", {
    class: "btn small", "aria-label": "Add to crate", style: "width:auto",
    onfocus: () => loadCrates().then(() => addSel.replaceChildren(...crateChoices())).catch(() => {}),
    onchange: (e) => { addToCrate(e.target.value); e.target.value = ""; },
  }, crateChoices());
  head.replaceChildren(
    h("h2", {}, d.title || "Untitled"),
    h("span", { class: "meta" }, meta, r?.url ? [" ", h("a", { href: r.url, target: "_blank", rel: "noopener" }, "Discogs page")] : null),
    h("div", { class: "editor-actions" },
      isDirty() ? h("button", { class: "btn small", type: "button", onclick: revertEdits }, "Undo edits") : null,
      S.current.origin.kind === "crate"
        ? h("button", { class: "btn small", type: "button", onclick: saveCrateEdits }, isDirty() ? "Save edits to crate" : "Saved in crate")
        : null,
      d.release_id ? addSel : null,
      d.release_id ? h("button", { class: "btn small quiet", type: "button", onclick: () => loadRef(String(d.release_id)), title: "Reload from Discogs" }, "Reload") : null),
  );
}

function renderEditor() {
  const box = $("#editor");
  if (!S.current) {
    box.replaceChildren(h("div", { class: "editor-head" },
      h("button", { class: "btn", type: "button", onclick: () => setCurrent(blankData(), null, { kind: "manual" }) }, "Start a blank label"),
      h("span", { class: "hint" }, "For a record that is not on Discogs, or a one-off.")));
    return;
  }
  const d = S.current.data;
  const text = (key, label, cls = "", attrs = {}) => h("label", { class: `field ${cls}` }, h("span", {}, label),
    h("input", { type: "text", value: d[key] ?? "", ...attrs, oninput: (e) => edit((x) => { x[key] = e.target.value; }) }));
  const genre = h("label", { class: "field w3" }, h("span", {}, "Genre (styles, comma separated)"),
    h("input", {
      type: "text", value: (d.styles?.length ? d.styles : d.genres || []).join(", "),
      oninput: (e) => edit((x) => { x.styles = e.target.value.split(",").map((s) => s.trim()).filter(Boolean); }),
    }));
  const fields = h("div", { class: "grid-fields" },
    text("artist", "Artist", "w3"), text("title", "Title", "w3"),
    text("label", "Record label", "w2"), text("catno", "Cat. no.", ""), text("year", "Year", "", { inputmode: "numeric" }),
    text("format", "Format", "", { list: "formats" }), text("country", "Country", ""),
    genre, text("qr_url", "QR code links to", "w3", { spellcheck: "false" }),
    h("label", { class: "field w6" }, h("span", {}, "Notes"),
      h("textarea", { rows: 2, oninput: (e) => edit((x) => { x.notes = e.target.value; }) }, d.notes || "")),
    h("datalist", { id: "formats" }, ['12"', '10"', '7"', "LP"].map((f) => h("option", { value: f }))),
  );
  const seller = h("details", { class: "more" }, h("summary", {}, "Price and condition (seller tags)"),
    h("div", { class: "grid-fields" }, text("price", "Price", "w2"), text("condition", "Condition", "w2")));
  box.replaceChildren(h("div", { class: "editor-head", id: "editor-head" }), fields, seller, tracksEditor());
  renderEditorHead();
}

function tracksEditor() {
  const d = S.current.data;
  d.tracks = d.tracks || [];
  const rerender = () => { fitTemplateToTracks(); $("#tracks").replaceWith(tracksEditor()); renderOptions(currentTemplate()); schedulePreview(); renderEditorHead(); };
  const cell = (t, key, cls, attrs = {}) => h("td", { class: cls },
    h("input", {
      type: attrs.type || "text", value: t[key] ?? "", "aria-label": key, ...attrs,
      oninput: (e) => edit(() => {
        const v = e.target.value;
        t[key] = attrs.type === "number" ? (v === "" ? null : Number(v)) : (key === "key" && v === "" ? null : v);
      }),
    }));
  // BPM: the number, plus a button that detects it from a file or by listening. An audio
  // file dropped on the row does the same.
  const bpmCell = (t) => {
    const td = cell(t, "bpm", "c-bpm", { type: "number", min: 1, max: 400 });
    const input = $("input", td);
    applyTempoState(input, t);
    const detect = h("button", {
      class: "bpm-detect", type: "button", title: "Detect the BPM from the release's Discogs videos, an audio file or by listening",
      "aria-label": `Detect the BPM of ${t.title || t.position || "this track"}`, "aria-haspopup": "dialog",
      onclick: () => openTempoDialog(t, input, detect),
    }, "♩");
    td.replaceChildren(h("div", { class: "bpm-cell" }, input, detect));
    return { td, input, detect };
  };
  const hasFiles = (e) => [...(e.dataTransfer?.types || [])].includes("Files");
  const rows = d.tracks.map((t, i) => {
    const bpm = bpmCell(t);
    return h("tr", {
      class: t.included === false ? "off" : "",
      ondragover: (e) => { if (hasFiles(e)) { e.preventDefault(); e.dataTransfer.dropEffect = "copy"; e.currentTarget.classList.add("drop-audio"); } },
      ondragleave: (e) => { if (!e.currentTarget.contains(e.relatedTarget)) e.currentTarget.classList.remove("drop-audio"); },
      ondrop: (e) => {
        const file = e.dataTransfer?.files?.[0];
        e.currentTarget.classList.remove("drop-audio");
        if (!file) return;
        e.preventDefault();
        openTempoDialog(t, bpm.input, bpm.detect, { file });
      },
    },
    h("td", { class: "c-in" }, h("input", {
      type: "checkbox", checked: t.included !== false, "aria-label": `Print ${t.title || "track"}`,
      onchange: (e) => { t.included = e.target.checked; rerender(); },
    })),
    cell(t, "position", "c-pos"), cell(t, "title", ""), cell(t, "artist", ""),
    cell(t, "duration", "c-dur"), bpm.td,
    cell(t, "key", "c-key", { placeholder: "8A" }),
    h("td", { class: "c-x" }, h("button", { class: "btn small quiet", type: "button", "aria-label": "Remove track", onclick: () => { d.tracks.splice(i, 1); rerender(); } }, "×")));
  });
  return h("section", { id: "tracks" },
    h("h3", { class: "section-title" }, h("span", { class: "tracks-title" }, "Tracks", tempoAutoToggle()),
      h("span", {},
        h("button", { class: "btn small quiet", type: "button", onclick: () => { d.tracks.forEach((t) => { t.included = true; }); rerender(); } }, "Tick all"),
        h("button", { class: "btn small quiet", type: "button", onclick: () => { d.tracks.forEach((t) => { t.included = false; }); rerender(); } }, "Untick all"),
        h("button", { class: "btn small", type: "button", onclick: () => { d.tracks.push({ position: "", title: "", artist: "", duration: "", bpm: null, key: null, included: true }); rerender(); } }, "Add track"))),
    d.tracks.length
      ? h("table", { class: "tracks" },
        h("thead", {}, h("tr", {}, h("th", { class: "c-in" }, h("span", { class: "sr-only" }, "Print")), h("th", {}, "Pos."), h("th", {}, "Title"), h("th", {}, "Artist"), h("th", {}, "Time"), h("th", {}, "BPM"), h("th", {}, "Key"), h("th", {}))),
        h("tbody", {}, rows))
      : h("p", { class: "hint" }, "No tracks. The label uses the release title."),
    h("p", { class: "hint", id: "fit-note" }, fitNoteText()));
}

function revertEdits() {
  S.current.data = structuredClone(S.current.original);
  renderEditor();
  schedulePreview();
}

// --- labels page: BPM detection ---------------------------------------------------------
// POST /api/tempo takes the audio itself (application/octet-stream): a file the user
// picks or drops, or a WAV recorded here from a line-in for records that only exist on
// vinyl. Nothing leaves the Mac.

const LISTEN_MAX_S = 45;
const LISTEN_MIN_S = 10;

const tempoStyles = () => {
  const d = S.current?.data || {};
  return [...(d.styles || []), ...(d.genres || [])].join(",");
};

// speedReminder reads the speed off the release's format ("Vinyl, 12\", 45 RPM").
function speedReminder() {
  const text = [S.current?.release?.format, S.current?.data?.format].filter(Boolean).join(" ");
  const speeds = [...new Set([...text.matchAll(/\b(78|45|33)\s*(?:⅓|1\/3)?\s*RPM/gi)].map((m) => (m[1] === "33" ? "33⅓" : m[1])))];
  return speeds.length ? `Play at ${speeds.join(" or ")} RPM, pitch at 0.` : "Set the pitch to 0 before you start.";
}

// The capture worklet mixes the input to mono and posts it in blocks, with each block's peak.
const CAPTURE_WORKLET = `
registerProcessor("crate-labels-capture", class extends AudioWorkletProcessor {
  constructor() { super(); this.buf = new Float32Array(4096); this.n = 0; this.peak = 0; }
  process(inputs) {
    const chans = inputs[0];
    if (!chans || !chans.length) return true;
    const len = chans[0].length;
    for (let i = 0; i < len; i++) {
      let sum = 0;
      for (const c of chans) { sum += c[i]; this.peak = Math.max(this.peak, Math.abs(c[i])); }
      this.buf[this.n++] = sum / chans.length;
      if (this.n === this.buf.length) {
        this.port.postMessage({ mono: this.buf, peak: this.peak }, [this.buf.buffer]);
        this.buf = new Float32Array(4096); this.n = 0; this.peak = 0;
      }
    }
    return true;
  }
});`;

// startCapture feeds onBlock(mono Float32Array, peak) from a MediaStream until stopped.
async function startCapture(ac, stream, onBlock) {
  const src = ac.createMediaStreamSource(stream);
  const mute = ac.createGain();
  mute.gain.value = 0; // pulled by the destination, never heard
  mute.connect(ac.destination);
  let node;
  if (ac.audioWorklet && typeof AudioWorkletNode === "function") {
    const url = URL.createObjectURL(new Blob([CAPTURE_WORKLET], { type: "text/javascript" }));
    try { await ac.audioWorklet.addModule(url); } finally { URL.revokeObjectURL(url); }
    node = new AudioWorkletNode(ac, "crate-labels-capture");
    node.port.onmessage = (e) => onBlock(e.data.mono, e.data.peak);
  } else {
    node = ac.createScriptProcessor(4096, 2, 1);
    node.onaudioprocess = (e) => {
      const b = e.inputBuffer;
      const mono = new Float32Array(b.length);
      let peak = 0;
      for (let c = 0; c < b.numberOfChannels; c++) {
        const x = b.getChannelData(c);
        for (let i = 0; i < x.length; i++) { mono[i] += x[i] / b.numberOfChannels; peak = Math.max(peak, Math.abs(x[i])); }
      }
      onBlock(mono, peak);
    };
  }
  src.connect(node);
  node.connect(mute);
  await ac.resume().catch(() => {});
  return () => { src.disconnect(); node.disconnect(); if (node.port) node.port.onmessage = null; };
}

// wavFile encodes mono float blocks as a 16-bit PCM WAV at rate (the server resamples).
function wavFile(blocks, frames, rate) {
  const buf = new ArrayBuffer(44 + frames * 2);
  const v = new DataView(buf);
  const ascii = (at, s) => { for (let i = 0; i < s.length; i++) v.setUint8(at + i, s.charCodeAt(i)); };
  ascii(0, "RIFF"); v.setUint32(4, 36 + frames * 2, true); ascii(8, "WAVE");
  ascii(12, "fmt "); v.setUint32(16, 16, true); v.setUint16(20, 1, true); v.setUint16(22, 1, true);
  v.setUint32(24, rate, true); v.setUint32(28, rate * 2, true); v.setUint16(32, 2, true); v.setUint16(34, 16, true);
  ascii(36, "data"); v.setUint32(40, frames * 2, true);
  let at = 44;
  for (const b of blocks) {
    for (let i = 0; i < b.length && at < buf.byteLength; i++, at += 2) {
      const s = Math.max(-1, Math.min(1, b[i]));
      v.setInt16(at, s < 0 ? s * 0x8000 : s * 0x7fff, true);
    }
  }
  return new Blob([buf], { type: "audio/wav" });
}

function micError(err) {
  switch (err?.name) {
    case "NotAllowedError": case "SecurityError":
      return "The browser may not use the audio input. Allow it for this page (and for your browser in System Settings → Privacy & Security → Microphone), then try again.";
    case "NotFoundError": case "OverconstrainedError":
      return "No audio input found. Plug in the line-in or microphone, then try again.";
    case "NotReadableError":
      return "That input is busy or unavailable; another app may be using it.";
  }
  return `Could not start listening: ${err?.message || err}`;
}

const mmss = (s) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, "0")}`;

// openTempoDialog detects one track's BPM. input is its BPM field, trigger the button
// that opened it; file starts the analysis of a dropped file straight away.
function openTempoDialog(track, input, trigger, { file } = {}) {
  $("dialog.tempo-dialog")?.close();
  const st = { ctl: null, stream: null, ac: null, stopCapture: null, timer: null, blocks: [], frames: 0, peak: 0, level: 0, closed: false, focusAfter: trigger };
  const label = [track.position, track.title].filter(Boolean).join(" ");
  const fileBtn = h("button", { class: "btn small", type: "button", "aria-pressed": "false", onclick: () => showFile() }, "From a file…");
  const listenBtn = h("button", { class: "btn small", type: "button", "aria-pressed": "false", onclick: () => showListen() }, "Listen…");
  const vids = releaseVideos();
  const videoBtn = vids.length ? h("button", { class: "btn small", type: "button", "aria-pressed": "false", onclick: () => showVideos() }, "Discogs video") : null;
  const body = h("div", { class: "tempo-body" });
  const result = h("div", { class: "tempo-result", "aria-live": "polite" });
  const dlg = h("dialog", { class: "tempo-dialog", "aria-labelledby": "tempo-h" },
    h("div", { class: "tempo-head" },
      h("h2", { id: "tempo-h" }, "Detect BPM", label ? h("small", {}, label) : null),
      h("button", { class: "btn small quiet", type: "button", "aria-label": "Close", onclick: () => dlg.close() }, "×")),
    h("div", { class: "tempo-ways", role: "group", "aria-label": "Detect from" }, videoBtn, fileBtn, listenBtn),
    body, result);
  dlg.addEventListener("dragover", (e) => { if ([...(e.dataTransfer?.types || [])].includes("Files")) e.preventDefault(); });
  dlg.addEventListener("drop", (e) => { const f = e.dataTransfer?.files?.[0]; if (f) { e.preventDefault(); analyseFile(f); } });
  dlg.addEventListener("close", () => {
    st.closed = true;
    st.ctl?.abort();
    stopListening();
    dlg.remove();
    if (st.focusAfter?.isConnected) st.focusAfter.focus();
  });
  document.body.append(dlg);
  dlg.showModal();

  const choose = (btn) => { for (const b of [videoBtn, fileBtn, listenBtn]) b?.setAttribute("aria-pressed", String(b === btn)); };
  const say = (text, cls = "hint") => result.replaceChildren(h("p", { class: cls, role: cls === "tempo-error" ? "alert" : null }, text));

  function showVideos() {
    stopListening();
    choose(videoBtn);
    const mine = trackVideo(track);
    const ordered = mine ? [mine, ...vids.filter((v) => v !== mine)] : vids;
    const list = ordered.map((v) => h("button", { class: `btn tempo-video${v === mine ? " mine" : ""}`, type: "button", onclick: () => analyseVideo(v) },
      h("span", {}, v.title || v.url),
      h("small", {}, [v.duration ? mmss(v.duration) : "", v === mine ? "this track" : v.track ? `track ${v.track}` : ""].filter(Boolean).join(" · "))));
    body.replaceChildren(
      h("p", { class: "hint" }, "YouTube videos linked on the release's Discogs page. The audio is fetched, analysed and thrown away."),
      h("div", { class: "tempo-videos" }, list));
    result.replaceChildren();
    list[0]?.focus();
  }

  async function analyseVideo(v) {
    stopListening();
    st.ctl?.abort();
    const mine = (st.videoRun = (st.videoRun || 0) + 1);
    say(`Fetching “${v.title || "the video"}” and working out its tempo…`, "hint busy");
    try {
      const r = await videoTempo(v);
      if (st.closed || mine !== st.videoRun) return;
      if (S.current?.data.tracks.includes(track)) S.current.tempo.set(track, { state: "done", video: v, result: r });
      showResult(r, Boolean(tempoStyles()));
    } catch (err) {
      if (!st.closed && mine === st.videoRun) say(err.message, "tempo-error");
    }
  }

  function showFile() {
    stopListening();
    choose(fileBtn);
    const picker = h("input", {
      type: "file", accept: "audio/*,.aif,.aiff,.flac", hidden: true,
      onchange: (e) => { if (e.target.files[0]) analyseFile(e.target.files[0]); },
    });
    const pick = h("button", { class: "btn", type: "button", onclick: () => picker.click() }, "Choose an audio file…");
    body.replaceChildren(
      h("p", { class: "hint" }, "WAV, AIFF, MP3, AAC/M4A, ALAC or FLAC of this track. You can also drop the file on its row."),
      h("div", { class: "tempo-acts" }, pick, picker));
    result.replaceChildren();
    pick.focus();
  }

  function analyseFile(f) {
    stopListening();
    choose(fileBtn);
    body.replaceChildren(h("p", { class: "tempo-file" }, f.name));
    analyse(f, f.name);
  }

  async function analyse(blob, name) {
    st.ctl?.abort();
    const ctl = (st.ctl = new AbortController());
    say(`Analysing ${name}…`, "hint busy");
    const styles = tempoStyles();
    try {
      const res = await fetch(`/api/tempo${styles ? `?styles=${enc(styles)}` : ""}`, {
        method: "POST", body: blob, signal: ctl.signal,
        // The one upload that is not JSON. Header values must be Latin-1: percent-encode.
        headers: { "Content-Type": "application/octet-stream", "X-Filename": enc(name) },
      });
      if (!res.ok) throw await apiError(res);
      showResult((await res.json()).result, Boolean(styles));
    } catch (err) {
      if (err.name !== "AbortError" && !st.closed) say(err.message, "tempo-error");
    }
  }

  function showResult(r, styled) {
    const word = r.confidence >= 0.7 ? "sure" : r.confidence >= 0.4 ? "likely" : "check this";
    const pick = (bpm, text, primary) => h("button", { class: `btn${primary ? " primary" : ""}`, type: "button", onclick: () => apply(bpm) }, text);
    const best = pick(r.bpm, `${r.bpm.toFixed(1)} BPM`, true);
    result.replaceChildren(
      h("p", { class: "tempo-answer" }, h("strong", {}, r.bpm.toFixed(1)), " BPM"),
      h("p", { class: "tempo-facts" },
        h("span", { class: "conf", "data-level": word === "sure" ? "high" : word === "likely" ? "mid" : "low" }, word),
        `, ${r.steady ? "steady" : "tempo varies"}, from ${mmss(r.seconds)} of audio`),
      h("div", { class: "tempo-choices", role: "group", "aria-label": "Use a value" }, best,
        (r.alternatives || []).slice(0, 5).map((a) => pick(a.bpm, `${a.bpm.toFixed(1)}${a.relation ? ` (${a.relation})` : ""}`))),
      h("p", { class: "hint" }, `Preferred ${Math.round(r.range[0])}–${Math.round(r.range[1])} BPM, ${styled ? "from this record's styles" : "the dance-music default"}. The label gets the whole number.`));
    best.focus();
  }

  function apply(bpm) {
    if (!S.current?.data.tracks.includes(track)) { say("This track changed in the meantime; open Detect again.", "tempo-error"); return; }
    const v = Math.round(bpm);
    edit(() => { track.bpm = v; });
    if (input.isConnected) { input.value = String(v); st.focusAfter = input; }
    dlg.close();
  }

  function showListen() {
    stopListening();
    choose(listenBtn);
    result.replaceChildren();
    if (!navigator.mediaDevices?.getUserMedia) {
      body.replaceChildren(h("p", { class: "tempo-error", role: "alert" }, "This browser can't capture audio on this page."));
      return;
    }
    const devices = h("select", { id: "tempo-input", onchange: (e) => { S.bpmInput = e.target.value; saveUi(); } });
    const meter = h("div", { class: "meter", "aria-hidden": "true" }, h("i"));
    const levelText = h("span", { class: "tempo-level", role: "status" }, "");
    const count = h("span", { class: "tempo-count" }, `up to ${LISTEN_MAX_S} s`);
    const start = h("button", { class: "btn primary", type: "button", onclick: () => startListening() }, "Start listening");
    const now = h("button", { class: "btn", type: "button", disabled: true, onclick: () => finishListening() }, `Analyse now`);
    st.ui = { devices, meter, levelText, count, start, now };
    body.replaceChildren(
      h("p", { class: "tempo-speed" }, speedReminder()),
      h("label", { class: "field" }, h("span", {}, "Input (line-in or microphone)"), devices),
      h("div", { class: "tempo-meter" }, meter, levelText),
      h("div", { class: "tempo-acts" }, start, now, count),
      h("p", { class: "hint" }, `Records ${LISTEN_MIN_S}–${LISTEN_MAX_S} s, then works out the tempo. A busy section with drums works best.`));
    fillDevices();
    start.focus();
  }

  async function fillDevices(activeId) {
    const sel = st.ui?.devices;
    if (!sel) return;
    let inputs = [];
    try { inputs = (await navigator.mediaDevices.enumerateDevices()).filter((d) => d.kind === "audioinput" && d.deviceId && d.deviceId !== "default"); } catch (_) { /* none */ }
    const want = activeId || S.bpmInput;
    sel.replaceChildren(h("option", { value: "" }, "System default input"),
      inputs.map((d, i) => h("option", { value: d.deviceId, selected: d.deviceId === want }, d.label || `Input ${i + 1}`)));
  }

  async function startListening() {
    stopListening();
    result.replaceChildren();
    const { devices, start, now, count } = st.ui;
    const constraints = (id) => ({ audio: { ...(id ? { deviceId: { exact: id } } : {}), echoCancellation: false, noiseSuppression: false, autoGainControl: false, channelCount: 2 } });
    start.disabled = true;
    try {
      try {
        st.stream = await navigator.mediaDevices.getUserMedia(constraints(devices.value));
      } catch (err) {
        if (!devices.value || !["OverconstrainedError", "NotFoundError"].includes(err.name)) throw err;
        st.stream = await navigator.mediaDevices.getUserMedia(constraints("")); // the saved input is gone
      }
      if (st.closed) { stopListening(); return; }
      // Labels appear once the browser may use an input.
      await fillDevices(st.stream.getAudioTracks()[0]?.getSettings().deviceId);
      const ac = (st.ac = new AudioContext());
      const stop = await startCapture(ac, st.stream, onBlock);
      if (st.ac !== ac) { stop(); return; } // closed or restarted meanwhile
      st.stopCapture = stop;
    } catch (err) {
      stopListening();
      if (st.closed) return;
      start.disabled = false;
      say(micError(err), "tempo-error");
      return;
    }
    start.textContent = "Listening…";
    const rate = st.ac.sampleRate;
    st.timer = setInterval(() => {
      const secs = st.frames / rate;
      count.textContent = `${mmss(Math.max(0, LISTEN_MAX_S - secs))} left`;
      now.disabled = secs < LISTEN_MIN_S;
      now.textContent = secs < LISTEN_MIN_S ? `Analyse now (in ${Math.ceil(LISTEN_MIN_S - secs)} s)` : "Analyse now";
    }, 250);
  }

  function onBlock(mono, peak) {
    if (!st.ac) return;
    st.blocks.push(mono);
    st.frames += mono.length;
    st.peak = Math.max(st.peak, peak);
    st.level = Math.max(peak, st.level * 0.85);
    const { meter, levelText } = st.ui;
    const db = 20 * Math.log10(Math.max(st.level, 1e-6));
    meter.firstChild.style.width = `${Math.max(0, Math.min(100, ((db + 60) / 60) * 100))}%`;
    const clip = peak >= 0.99;
    meter.classList.toggle("clip", clip);
    const word = clip ? "Clipping: turn the input down" : st.level < 0.01 ? "Very quiet" : "Level OK";
    if (levelText.textContent !== word) levelText.textContent = word;
    if (st.frames >= LISTEN_MAX_S * st.ac.sampleRate) finishListening();
  }

  function finishListening() {
    if (!st.ac) return;
    const rate = st.ac.sampleRate;
    const frames = Math.min(st.frames, LISTEN_MAX_S * rate);
    const { blocks, peak } = st;
    stopListening();
    if (frames < LISTEN_MIN_S * rate) { say(`Give it at least ${LISTEN_MIN_S} seconds.`, "tempo-error"); return; }
    if (peak < 0.003) { say("Nothing came in on this input. Check the cable, the input level and that the record is playing.", "tempo-error"); return; }
    analyse(wavFile(blocks, frames, rate), "listening.wav");
  }

  function stopListening() {
    clearInterval(st.timer);
    st.timer = null;
    st.stopCapture?.();
    st.stopCapture = null;
    st.stream?.getTracks().forEach((t) => t.stop());
    st.stream = null;
    st.ac?.close().catch(() => {});
    st.ac = null;
    st.blocks = [];
    st.frames = st.peak = st.level = 0;
    if (st.ui?.start?.isConnected) {
      st.ui.start.disabled = false;
      st.ui.start.textContent = "Start listening";
      st.ui.now.disabled = true;
      st.ui.now.textContent = "Analyse now";
      st.ui.count.textContent = `up to ${LISTEN_MAX_S} s`;
      st.ui.meter.firstChild.style.width = "0";
      st.ui.levelText.textContent = "";
    }
  }

  const done = S.current?.tempo.get(track);
  if (file) analyseFile(file);
  else if (done?.state === "done") { showVideos(); showResult(done.result, Boolean(tempoStyles())); }
  else if (trackVideo(track)) showVideos();
  else showFile();
}

// --- BPM from the release's Discogs videos ----------------------------------------------
// Releases often link a YouTube video per track. When a record opens, the server fetches
// each video's audio (yt-dlp), analyses it and throws it away; the BPM goes in the empty
// BPM fields one track at a time. Results are cached by video, so reopening is instant.

const confWord = (c) => (c >= 0.7 ? "sure" : c >= 0.4 ? "likely" : "check this");
const tempoStyleList = () => tempoStyles().split(",").filter(Boolean);
const releaseVideos = () => S.current?.release?.videos || [];
const trackVideo = (t) => releaseVideos().find((v) => v.track && v.track === t.position) || null;

function videoTempo(v) {
  return api("/api/tempo/video", { method: "POST", body: { url: v.url, styles: tempoStyleList() } }).then((r) => r.result);
}

let videoRun = 0;

async function autoTempoFromVideos() {
  const cur = S.current;
  if (!cur || !S.bpmVideos) return;
  const run = ++videoRun;
  const todo = (cur.data.tracks || []).filter((t) => !(t.bpm > 0) && trackVideo(t) && !cur.tempo.has(t));
  for (const t of todo) {
    if (run !== videoRun || S.current !== cur || !S.bpmVideos) return;
    const v = trackVideo(t);
    cur.tempo.set(t, { state: "busy", video: v });
    refreshTempoCells();
    try {
      const r = await videoTempo(v);
      cur.tempo.set(t, { state: "done", video: v, result: r });
      if (!(t.bpm > 0) && cur.data.tracks.includes(t)) {
        t.bpm = Math.round(r.bpm);
        // A detected BPM belongs to the record, like the Discogs data: not an edit to undo.
        const o = (cur.original.tracks || []).find((x) => x.position === t.position);
        if (o && !(o.bpm > 0)) o.bpm = t.bpm;
        if (S.current === cur) schedulePreview();
      }
    } catch (err) {
      cur.tempo.set(t, { state: "error", video: v, error: err.message });
      if (/yt-dlp/.test(err.message)) { if (S.current === cur) toast(err.message, "error"); break; }
    }
    if (S.current === cur) refreshTempoCells();
  }
}

// applyTempoState shows a track's video analysis on its BPM field.
function applyTempoState(input, t) {
  const st = S.current?.tempo.get(t);
  if (document.activeElement !== input) input.value = t.bpm ?? "";
  input.placeholder = st?.state === "busy" ? "…" : "";
  input.classList.toggle("tempo-busy", st?.state === "busy");
  input.classList.toggle("tempo-check", st?.state === "done" && st.result.confidence < 0.4 && Math.round(st.result.bpm) === t.bpm);
  input.title = !st ? ""
    : st.state === "busy" ? `Working out the BPM from the video “${st.video.title}”…`
    : st.state === "done" ? `${st.result.bpm.toFixed(2)} BPM from the video “${st.video.title}” (${confWord(st.result.confidence)}). Click ♩ for other readings.`
    : `No BPM from the video “${st.video.title}”: ${st.error}`;
}

function refreshTempoCells() {
  const cur = S.current;
  if (!cur) return;
  const rows = [...document.querySelectorAll("#tracks tbody tr")];
  (cur.data.tracks || []).forEach((t, i) => {
    const input = rows[i]?.querySelector("td.c-bpm input");
    if (input) applyTempoState(input, t);
  });
  const status = $("#tempo-auto-status");
  if (status) status.textContent = videoStatus();
}

function videoStatus() {
  const cur = S.current;
  const vids = releaseVideos();
  if (!cur || !vids.length) return "";
  const states = (cur.data.tracks || []).map((t) => cur.tempo.get(t)?.state).filter(Boolean);
  const busy = states.filter((s) => s === "busy").length;
  if (busy) return `analysing ${states.length} of ${(cur.data.tracks || []).filter((t) => trackVideo(t)).length}…`;
  return `${vids.length} video${vids.length === 1 ? "" : "s"} on Discogs`;
}

function tempoAutoToggle() {
  if (!releaseVideos().length) return null;
  return h("label", { class: "tempo-auto", title: "Fetch each track's YouTube video linked on Discogs and fill in its BPM" },
    h("input", { type: "checkbox", checked: S.bpmVideos, onchange: (e) => { S.bpmVideos = e.target.checked; saveUi(); if (S.bpmVideos) autoTempoFromVideos(); } }),
    " BPM from Discogs videos ", h("span", { id: "tempo-auto-status", class: "hint" }, videoStatus()));
}

async function addToCrate(crateId) {
  if (!crateId || !S.current) return;
  try {
    if (crateId === "+new") {
      const name = prompt("Name for the new crate (leave it blank to name it later)", "");
      if (name === null) return;
      crateId = (await api("/api/crates", { method: "POST", body: { name } })).id;
    }
    const body = isDirty() ? { data: S.current.data } : { release_id: S.current.data.release_id };
    const detail = await api(`/api/crates/${enc(crateId)}/records`, { method: "POST", body });
    await loadCrates();
    if (S.crate?.id === crateId) { S.crate = detail; S.selection.add(detail.records.at(-1).id); }
    if (S.left === "crates") renderLeft();
    toast(`Added to ${detail.name} (${plural(detail.records.length, "record")})`);
  } catch (err) { toast(err.message, "error"); }
}

async function saveCrateEdits() {
  const o = S.current.origin;
  try {
    S.crate = await api(`/api/crates/${enc(o.crateId)}/records/${o.recordId}/data`, { method: "PUT", body: { data: S.current.data } });
    S.current.original = structuredClone(S.current.data);
    renderEditorHead();
    if (S.left === "crates") renderLeft();
    toast("Saved to the crate");
  } catch (err) { toast(err.message, "error"); }
}

// --- labels page: printing, queue, history ----------------------------------------------------

const copies = () => Math.max(1, Math.min(99, Number($("#copies").value) || 1));

function requirePrinter() {
  if (S.printerId) return true;
  toast("Add a printer first", "error");
  location.hash = "#/printers";
  return false;
}

function renderPrintNote() {
  const note = $("#print-note");
  const p = S.printers.find((x) => x.id === S.printerId);
  note.classList.toggle("warn", !p);
  note.textContent = p ? `${p.model}${p.address ? `, ${p.address}` : ""}` : "Previews work without a printer. Add one on the Printers page to print.";
}

async function printCurrent() {
  if (!requirePrinter()) return;
  if (!S.current) { toast("Load a record first: paste a Discogs link or search", "error"); $("#q").focus(); return; }
  if (!S.template || !S.stock) { toast("Choose a template and stock that fit this printer", "error"); return; }
  const btn = $("#print");
  btn.disabled = true;
  try {
    const job = await api("/api/print", {
      method: "POST",
      body: { items: [{ data: S.current.data }], template: S.template, stock: S.stock, printer_id: S.printerId, copies: copies(), options: currentOptions() },
    });
    upsertJob(job);
    toast(`Queued on ${job.printer_name}`);
  } catch (err) {
    toast(err.message, "error");
  } finally {
    btn.disabled = false;
  }
}

function upsertJob(job) {
  const before = S.jobs.get(job.id);
  S.jobs.set(job.id, job);
  if (before && before.status !== job.status) {
    if (job.status === "done") { toast(job.message || `Printed ${plural(job.printed, "label")}`); refreshAfterPrint(); }
    if (job.status === "failed") { toast(`${job.printer_name}: ${job.error || "print failed"}`, "error"); refreshAfterPrint(); }
  }
  renderJobs();
}

const refreshAfterPrint = debounce(() => {
  loadHistory();
  if (S.left === "crates" && S.crate) openCrate(S.crate.id);
  if (S.left === "results" && S.results.rows.length) {
    const ids = S.results.rows.map((r) => r.id).join(",");
    api(`/api/printed?release_ids=${ids}`).then(({ releases }) => {
      for (const r of S.results.rows) r.printed = Boolean(releases[r.id]?.printed);
      if (S.left === "results") renderLeft();
    }).catch(() => {});
  }
}, 300);

function renderJobs() {
  const jobs = [...S.jobs.values()].sort((a, b) => b.id - a.id);
  const active = jobs.filter((j) => j.status === "queued" || j.status === "printing");
  const recent = jobs.filter((j) => !active.includes(j)).slice(0, 6);
  $("#queue-count").textContent = active.length ? `${active.length} waiting` : "";
  const list = [...active, ...recent];
  $("#jobs").replaceChildren(...(list.length ? list.map(jobItem) : [h("li", { class: "hint", style: "display:block" }, "Nothing printing. Jobs show here with live progress.")]));
}

function jobItem(j) {
  const sub = j.status === "printing"
    ? j.message
    : j.status === "queued" ? `Waiting for ${j.printer_name}`
      : j.status === "done" ? `${plural(j.printed, "label")} on ${j.printer_name}, ${relTime(j.finished_at || j.created_at)}`
        : `${j.status === "failed" ? "Failed" : j.status === "interrupted" ? "Interrupted" : "Cancelled"} after ${j.printed} of ${j.total}`;
  const title = j.records > 1 ? `${plural(j.records, "record")}${j.source.startsWith("crate:") ? ` from ${j.source.slice(6)}` : ""}` : (j.title || "Label");
  const canCancel = j.status === "queued" || j.status === "printing";
  const canRetry = ["failed", "cancelled", "interrupted"].includes(j.status);
  return h("li", {},
    h("span", { class: "dot", "data-status": j.status, title: j.status }),
    h("div", { class: "what" },
      h("b", {}, title),
      h("span", {}, sub),
      j.error && j.status !== "done" ? h("span", { class: "err" }, j.error) : null,
      j.status === "printing" ? h("div", { class: "progress", role: "progressbar", "aria-valuenow": Math.round(j.progress * 100), "aria-valuemin": 0, "aria-valuemax": 100 }, h("i", { style: `width:${Math.round(j.progress * 100)}%` })) : null),
    h("div", { class: "acts" },
      canCancel ? h("button", { class: "btn small quiet", type: "button", onclick: () => jobAction(j, "cancel") }, "Cancel") : null,
      canRetry ? h("button", { class: "btn small quiet", type: "button", onclick: () => jobAction(j, "retry") }, "Retry") : null));
}

async function jobAction(job, action) {
  try { upsertJob(await api(`/api/jobs/${job.id}/${action}`, { method: "POST" })); } catch (err) { toast(err.message, "error"); }
}

async function loadJobs() {
  try {
    const { jobs } = await api("/api/jobs?limit=12");
    for (const j of jobs.reverse()) S.jobs.set(j.id, j);
    renderJobs();
  } catch (_) { /* server restarting */ }
}

function connectEvents() {
  const es = new EventSource("/api/jobs/events");
  es.addEventListener("job", (e) => upsertJob(JSON.parse(e.data)));
  // Polling backs the stream up while anything is active (e.g. after a reconnect).
  setInterval(() => {
    if ([...S.jobs.values()].some((j) => j.status === "queued" || j.status === "printing")) loadJobs();
  }, 4000);
}

async function loadHistory() {
  try {
    const data = await api("/api/history?limit=40");
    S.history = data.history;
    S.labelsPrinted = data.labels_printed;
  } catch (_) { return; }
  $("#labels-printed").textContent = S.labelsPrinted ? `${S.labelsPrinted} so far` : "";
  $("#history").replaceChildren(...(S.history.length ? S.history.map(historyItem) : [h("li", { class: "hint", style: "display:block" }, "Labels you print are kept here for reprinting.")]));
}

function historyItem(e) {
  const tpl = S.templates.find((t) => t.id === e.template);
  return h("li", {},
    h("span", { class: "dot history", title: "Printed" }),
    h("button", { class: "open what", type: "button", title: "Open in the editor", onclick: () => openHistory(e) },
      h("b", {}, [e.artist, e.title].filter(Boolean).join(" – ") || "Label"),
      h("span", {}, `${[e.catno, tpl ? tpl.name : e.template].filter(Boolean).join(", ")}, ${e.copies > 1 ? `${e.copies}×, ` : ""}${relTime(e.printed_at)}`)),
    h("div", { class: "acts" }, h("button", { class: "btn small quiet", type: "button", onclick: () => reprint(e) }, "Reprint")));
}

async function openHistory(e) {
  if (usableTemplates().some((t) => t.id === e.template)) {
    S.template = e.template;
    S.options[e.template] = { ...(S.options[e.template] || {}), ...e.options };
    renderDesignBar();
    if (stocksForTemplate(currentTemplate()).some((s) => s.id === e.stock)) { S.stock = e.stock; renderDesignBar(); }
  }
  setCurrent(e.data, null, { kind: "history", id: e.id });
  if (e.release_id) loadReleaseSummary(e.release_id);
}

async function reprint(e) {
  if (!requirePrinter()) return;
  try {
    const job = await api(`/api/history/${e.id}/reprint`, { method: "POST", body: { printer_id: S.printerId } });
    upsertJob(job);
    toast(`Reprinting on ${job.printer_name}`);
  } catch (err) { toast(err.message, "error"); }
}

// --- printers page -------------------------------------------------------------------------

function modelOptions(filter, selected) {
  return S.models.filter(filter).map((m) => h("option", { value: m.id, selected: m.id === selected }, m.label));
}

function bindPrintersPage() {
  $("#ble-scan").addEventListener("click", bleScan);
  $("#ipp-scan").addEventListener("click", ippScan);
  $("#cups-scan").addEventListener("click", cupsScan);
  $("#manual-add").addEventListener("submit", async (e) => {
    e.preventDefault();
    await savePrinter($("#manual-name").value, $("#manual-model").value, $("#manual-address").value);
    e.target.reset();
  });
}

function renderPrintersPage() {
  $("#manual-model").replaceChildren(...modelOptions(() => true));
  renderSavedPrinters();
}

function renderSavedPrinters() {
  const box = $("#saved-printers");
  if (!S.printers.length) {
    box.replaceChildren(h("p", { class: "status-line" }, "No printers yet. Find one below: Bluetooth label printers, Wi-Fi printers, or anything macOS can already print to."));
    return;
  }
  const test = S.templates.find((t) => t.id === "test-pattern");
  box.replaceChildren(...S.printers.map((p) => {
    const model = S.models.find((m) => m.id === p.model_id);
    const defaults = model?.options || {};
    const settings = { ...p.settings };
    const num = (key, label, attrs = {}) => h("label", { class: "field" }, h("span", {}, label),
      h("input", { type: "number", value: settings[key] ?? "", placeholder: defaults[key] ?? "", ...attrs, oninput: (e) => { settings[key] = e.target.value === "" ? null : Number(e.target.value); } }));
    const name = h("input", { type: "text", value: p.name, "aria-label": "Printer name" });
    const commitName = async () => { if (name.value.trim() && name.value !== p.name) await patchPrinter(p.id, { name: name.value.trim() }, "Renamed"); };
    name.addEventListener("change", commitName);
    const testStocks = model ? model.stocks.filter((id) => !test || test.stocks.includes(id)) : [];
    const testStock = h("select", { "aria-label": "Test pattern stock" }, testStocks.map((s) => h("option", { value: s }, s)));
    const family = model?.family || "";
    const thermal = ["phomemo", "munbyn", "clabel"].includes(family);
    const offsetHelp = family === "phomemo"
      ? "blank dots before the label, across the head; to centre the label, set Paper position instead"
      : "blank dots before the label, across the head";
    return h("div", { class: "printer" },
      h("div", { class: "printer-name" }, name,
        h("span", { class: "tag" }, h("b", {}, p.model), p.address ? `, ${p.address}` : ""),
        p.id === S.defaultPrinter ? h("span", { class: "tag" }, "Default") : h("button", { class: "btn small quiet", type: "button", onclick: () => setDefaultPrinter(p.id) }, "Make default")),
      h("div", { class: "printer-acts" },
        testStock,
        h("button", { class: "btn small", type: "button", disabled: !test || !testStocks.length, title: test ? "" : "This build has no test-pattern template", onclick: () => testPrint(p, testStock.value) }, "Print test pattern"),
        h("button", { class: "btn small quiet danger", type: "button", onclick: () => deletePrinter(p) }, "Remove")),
      thermal ? h("form", {
        class: "calib",
        onsubmit: async (e) => { e.preventDefault(); await patchPrinter(p.id, { settings }, "Calibration saved"); },
      },
      h("p", { class: "help" }, `Empty fields use the model's defaults. Rotation and offset move the print on the head; darkness and pacing trade speed for contrast and reliability. Offset: ${offsetHelp}.`),
      // Each driver reads its own names: Clabel calls darkness "density" (0-15).
      family === "clabel" ? num("density", "Density", { min: 0, max: 15 }) : num("darkness", "Darkness", { min: 1, max: 15 }),
      family !== "munbyn" ? num("speed", "Speed", { min: 1, max: 15 }) : null,
      h("label", { class: "field" }, h("span", {}, "Extra rotation"),
        h("select", { onchange: (e) => { settings.extra_rotate_cw = e.target.value === "" ? null : Number(e.target.value); } },
          h("option", { value: "" }, "None"), [90, 180, 270].map((d) => h("option", { value: d, selected: settings.extra_rotate_cw === d }, `${d}° clockwise`)))),
      num("offset_dots", "Offset (dots)", { min: 0, max: 600, title: offsetHelp }),
      family === "phomemo" ? paperPosition(settings, defaults) : null,
      model?.link === "ble" ? num("ble_gap_ms", "Bluetooth gap (ms)", { min: 0, max: 1000, step: 1 }) : null,
      h("div", {}, h("button", { class: "btn", type: "submit" }, "Save calibration"))) : null);
  }));
}

// paperPosition is Phomemo's paper_align setting: where narrow paper runs under a wider
// head, against the right edge or down the middle.
function paperPosition(settings, defaults) {
  const choices = [["right", "Against the right edge"], ["center", "In the middle"]];
  const named = (v) => (choices.find(([id]) => id === v) || [v, v])[1].toLowerCase();
  return h("label", { class: "field" }, h("span", {}, "Paper position"),
    h("select", { onchange: (e) => { settings.paper_align = e.target.value || null; } },
      h("option", { value: "" }, `As the model ships (${named(defaults.paper_align || "right")})`),
      choices.map(([v, t]) => h("option", { value: v, selected: settings.paper_align === v }, t))));
}

async function patchPrinter(id, body, message) {
  try {
    await api(`/api/printers/${enc(id)}`, { method: "PATCH", body });
    await loadPrinters();
    renderSavedPrinters();
    if (message) toast(message);
  } catch (err) { toast(err.message, "error"); }
}

async function setDefaultPrinter(id) {
  await api("/api/settings", { method: "PUT", body: { values: { default_printer: id } } });
  S.printerId = id;
  await loadPrinters();
  await refreshStocks();
  renderDesignBar();
  renderSavedPrinters();
  schedulePreview();
}

async function deletePrinter(p) {
  if (!confirm(`Remove ${p.name}? Its calibration is lost.`)) return;
  try {
    await api(`/api/printers/${enc(p.id)}`, { method: "DELETE" });
    await loadPrinters();
    await refreshStocks();
    renderDesignBar();
    renderSavedPrinters();
  } catch (err) { toast(err.message, "error"); }
}

async function testPrint(p, stock) {
  try {
    const job = await api(`/api/printers/${enc(p.id)}/test`, { method: "POST", body: { stock } });
    upsertJob(job);
    toast(`Test pattern queued on ${p.name}`);
  } catch (err) { toast(err.message, "error"); }
}

async function savePrinter(name, modelId, address) {
  try {
    const p = await api("/api/printers", { method: "POST", body: { name: name || "Printer", model_id: modelId, address: address || "" } });
    await loadPrinters();
    if (!S.printerId) S.printerId = p.id;
    await refreshStocks();
    renderDesignBar();
    renderSavedPrinters();
    schedulePreview();
    toast(`Saved ${p.name}`);
  } catch (err) { toast(err.message, "error"); }
}

function foundItem(title, sub, models, selected, address, extra = null) {
  const name = h("input", { type: "text", value: title, "aria-label": "Name" });
  const model = h("select", { "aria-label": "Model" }, models.length ? models : h("option", { value: "" }, "No matching model"));
  if (selected) model.value = selected;
  return h("li", {},
    h("strong", {}, title), extra || h("span", {}),
    h("span", { class: "sub" }, sub),
    h("div", { class: "save" }, name, model,
      h("button", { class: "btn small primary", type: "button", disabled: !models.length, onclick: () => savePrinter(name.value, model.value, address) }, "Save")));
}

async function bleScan() {
  const btn = $("#ble-scan");
  const list = $("#ble-found");
  btn.disabled = true;
  list.replaceChildren(h("li", { class: "sub" }, "Scanning…"));
  try {
    const { devices } = await api(`/api/printers/ble/scan?timeout=6&show_all=${$("#ble-all").checked}`);
    const bleModels = (ids) => modelOptions((m) => m.link === "ble" && (!ids.length || ids.includes(m.id)));
    list.replaceChildren(...(devices.length ? devices.map((d) => {
      const identify = h("button", { class: "btn small", type: "button" }, "Identify");
      const li = foundItem(d.name || "(no name)", `${d.address}, signal ${d.rssi} dBm${d.model_ids.length ? "" : d.maybe_family ? `, looks like ${d.maybe_family}` : ", unknown model"}`,
        bleModels(d.model_ids.length ? d.model_ids : []), d.model_ids[0], d.address, identify);
      identify.addEventListener("click", async () => {
        identify.disabled = true;
        identify.textContent = "Connecting…";
        try {
          const { probe } = await api("/api/printers/ble/probe", { method: "POST", body: { address: d.address } });
          if (probe.model_id) { $("select", li).value = probe.model_id; identify.textContent = "Identified"; } else identify.textContent = probe.family ? `${probe.family}, model unknown` : "Not recognised";
        } catch (err) { identify.textContent = "Identify"; identify.disabled = false; toast(err.message, "error"); }
      });
      return li;
    }) : [h("li", { class: "sub" }, "No label printers answered. Is it on and not connected to a phone?")]));
  } catch (err) {
    list.replaceChildren(h("li", { class: "sub" }, err.message));
  } finally { btn.disabled = false; }
}

async function ippScan() {
  const btn = $("#ipp-scan");
  const list = $("#ipp-found");
  btn.disabled = true;
  list.replaceChildren(h("li", { class: "sub" }, "Looking…"));
  try {
    const { printers } = await api("/api/printers/ipp/discover?timeout=5");
    list.replaceChildren(...(printers.length ? printers.map((p) => foundItem(p.name, `${p.model || "IPP printer"}, ${p.uri}${p.printable === false ? ", no supported format" : ""}`,
      modelOptions((m) => m.link === "ipp"), p.suggested_model_id || "ipp-sheet", p.uri))
      : [h("li", { class: "sub" }, "No IPP printers found on this network.")]));
  } catch (err) {
    list.replaceChildren(h("li", { class: "sub" }, err.message));
  } finally { btn.disabled = false; }
}

async function cupsScan() {
  const btn = $("#cups-scan");
  const list = $("#cups-found");
  btn.disabled = true;
  try {
    const { queues } = await api("/api/printers/cups/queues");
    list.replaceChildren(...(queues.length ? queues.map((q) => {
      const name = typeof q === "string" ? q : q.name;
      const desc = typeof q === "string" ? "" : [q.description, q.state, q.is_default ? "macOS default" : ""].filter(Boolean).join(", ");
      return foundItem(name, desc || "CUPS queue", modelOptions((m) => m.link === "cups"), "cups", name);
    }) : [h("li", { class: "sub" }, "macOS has no printers set up.")]));
  } catch (err) {
    list.replaceChildren(h("li", { class: "sub" }, err.message));
  } finally { btn.disabled = false; }
}

// --- settings page --------------------------------------------------------------------------

function bindSettingsPage() {
  $("#token-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const token = $("#token").value.trim();
    if (!token) return;
    await tokenAction(() => api("/api/settings/discogs-token", { method: "PUT", body: { token } }));
    $("#token").value = "";
  });
  $("#token-test").addEventListener("click", () => tokenAction(() => api("/api/settings/discogs-token/test", { method: "POST" })));
  $("#token-clear").addEventListener("click", async () => {
    if (!confirm("Remove the Discogs token from the Keychain?")) return;
    await tokenAction(() => api("/api/settings/discogs-token", { method: "DELETE" }));
  });
  $("#prefer-anv").addEventListener("change", (e) => {
    api("/api/settings", { method: "PUT", body: { values: { "discogs.prefer_anv": e.target.checked } } })
      .then(() => { S.settings["discogs.prefer_anv"] = e.target.checked; if (S.current?.data.release_id) loadRef(String(S.current.data.release_id), { quiet: true }); })
      .catch((err) => toast(err.message, "error"));
  });
  $("#theme-select").addEventListener("change", (e) => setTheme(e.target.value));
  $("#theme-toggle").addEventListener("click", () => {
    const dark = document.documentElement.dataset.theme === "dark" ||
      (!document.documentElement.dataset.theme && matchMedia("(prefers-color-scheme: dark)").matches);
    setTheme(dark ? "light" : "dark");
  });
}

function setTheme(theme) {
  if (theme === "auto") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = theme;
  try { localStorage.setItem("crate-labels.theme", theme); } catch (_) { /* private mode */ }
  api("/api/settings", { method: "PUT", body: { values: { theme } } }).catch(() => {});
  $("#theme-select").value = theme;
}

async function tokenAction(fn) {
  const status = $("#token-status");
  status.className = "status-line";
  status.textContent = "Checking with Discogs…";
  let result;
  try {
    result = await fn();
  } catch (err) {
    status.className = "status-line bad";
    status.textContent = err.message;
    applySettings(await api("/api/settings"));
    return;
  }
  applySettings(await api("/api/settings"));
  S.collection = { folder: 0, page: 1, sort: "artist", folders: null, data: null, error: "" };
  renderSettingsPage();
  if (result && result.warning) {
    // Saved without Discogs confirming it (e.g. offline): say so.
    status.className = "status-line bad";
    status.textContent = result.warning;
  }
}

function renderSettingsPage() {
  const d = S.discogs;
  const status = $("#token-status");
  if (!status.classList.contains("bad")) {
    status.className = `status-line ${d.token_set ? "ok" : ""}`;
    status.textContent = d.token_set
      ? `Token ${d.source === "env" ? "from the DISCOGS_TOKEN environment variable" : "in the Keychain"}${d.username ? `, signed in as ${d.username}` : ", not tested yet"}.`
      : "No token yet. Discogs needs one for lookups from 1 December 2026.";
  }
  $("#prefer-anv").checked = Boolean(S.settings["discogs.prefer_anv"]);
  $("#theme-select").value = document.documentElement.dataset.theme || "auto";
  const facts = [
    ["Data folder", S.dataDir || ""],
    ["Labels printed", String(S.labelsPrinted)],
    ["Templates", String(S.templates.length)],
    ["Printer drivers", (S.health?.drivers || []).join(", ") || "none loaded"],
  ];
  $("#facts").replaceChildren(...facts.flatMap(([k, v]) => [h("dt", {}, k), h("dd", {}, v)]));
}

boot().catch((err) => {
  console.error(err);
  toast(`crate-labels could not start: ${err.message}`, "error");
});
