// RecallRoute board. Plain JS, no build step. All server text is inserted
// with textContent: recall notices come from an outside feed.
"use strict";

const $ = (sel, root = document) => root.querySelector(sel);
const store = {
  get(k) { try { return localStorage.getItem(k) || ""; } catch { return ""; } },
  set(k, v) { try { localStorage.setItem(k, v); } catch { /* private mode */ } },
};

const actorInput = $("#actor");
const tokenInput = $("#token");
actorInput.value = store.get("rr.actor");
tokenInput.value = store.get("rr.token");
actorInput.addEventListener("change", () => store.set("rr.actor", actorInput.value.trim()));
tokenInput.addEventListener("change", () => store.set("rr.token", tokenInput.value.trim()));

let toastTimer;
function toast(msg, isErr = false) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = "show" + (isErr ? " err" : "");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.className = ""; t.textContent = ""; }, isErr ? 8000 : 4000);
}

async function api(method, path, body, contentType = "application/json") {
  const headers = {};
  if (body !== undefined) headers["Content-Type"] = contentType;
  const tok = tokenInput.value.trim();
  if (tok && method !== "GET") headers["Authorization"] = "Bearer " + tok;
  const res = await fetch(path, {
    method, headers,
    body: body === undefined ? undefined : (contentType === "application/json" && typeof body !== "string" ? JSON.stringify(body) : body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.error || res.statusText);
    err.code = data.code;
    err.status = res.status;
    throw err;
  }
  return data;
}

// ---------- board ----------

let alerts = [];

async function refresh() {
  try {
    alerts = await api("GET", "/api/alerts");
    render();
  } catch (e) {
    toast("Could not load alerts: " + e.message, true);
  }
}

function render() {
  const tpl = $("#card-tpl");
  document.querySelectorAll(".col").forEach((col) => {
    const state = col.dataset.state;
    const list = alerts.filter((a) => a.state === state);
    $(".n", col).textContent = list.length ? `(${list.length})` : "";
    const cards = $(".cards", col);
    cards.replaceChildren(...list.map((a) => {
      const el = tpl.content.firstElementChild.cloneNode(true);
      el.classList.add(a.tier);
      $(".tier", el).textContent = a.tier;
      $(".cls", el).textContent = `${a.recall.classification || ""} · ${a.recall_id}`;
      $(".product", el).textContent = a.lot.name;
      $(".meta", el).textContent = [
        a.lot.lot_code ? `Lot ${a.lot.lot_code}` : "no lot code",
        `${a.lot.quantity} on shelf`,
        a.lot.location,
      ].filter(Boolean).join(" · ");
      $(".why", el).textContent = a.recall.reason;
      el.addEventListener("click", () => openDetail(a.id));
      el.addEventListener("keydown", (ev) => { if (ev.key === "Enter") openDetail(a.id); });
      return el;
    }));
  });
  $("#empty").hidden = alerts.length > 0;
}

// ---------- detail + actions ----------

const ACTION_LABELS = {
  acknowledge: "Acknowledge", pull: "Pull from shelf", notify: "Record notifications",
  close: "Close", dismiss: "Dismiss (false match)",
};

function el(tag, props = {}, ...children) {
  const e = document.createElement(tag);
  Object.assign(e, props);
  for (const c of children) e.append(c);
  return e;
}

function kv(pairs) {
  const dl = el("dl", { className: "kv" });
  for (const [k, v] of pairs) {
    if (v === undefined || v === null || v === "") continue;
    dl.append(el("dt", { textContent: k }), el("dd", { textContent: String(v) }));
  }
  return dl;
}

function fmtDate(s) {
  if (!s || s.startsWith("0001-")) return "";
  return s.slice(0, 10);
}

async function openDetail(id) {
  let v;
  try {
    v = await api("GET", `/api/alerts/${id}`);
  } catch (e) {
    toast(e.message, true);
    return;
  }
  const body = $("#detail-body");
  body.replaceChildren(
    el("h2", { textContent: v.lot.name }),
    el("p", { className: "meta", textContent: `${v.tier.toUpperCase()} match · score ${v.score} · ${v.state}` }),
    kv([
      ["Recall", `${v.recall_id} (${v.recall.classification})`],
      ["Firm", v.recall.firm],
      ["Reason", v.recall.reason],
      ["Recalled product", v.recall.product],
      ["Code info", v.recall.code_info],
      ["Why matched", (v.reasons || []).join("; ")],
      ["Our lot", v.lot.lot_code || "(none recorded)"],
      ["Best by", fmtDate(v.lot.best_by) || "(none recorded)"],
      ["Barcode", v.lot.gtin || "(none recorded)"],
      ["On shelf", `${v.lot.quantity} at ${v.lot.location || "?"}`],
      ["Pulled so far", v.pulled_qty],
    ]),
    actionForm(v),
    el("h3", { textContent: "History" }),
    timeline(v.events),
  );
  const dlg = $("#detail");
  dlg.dataset.id = String(id);
  if (!dlg.open) dlg.showModal();
}

function actionForm(v) {
  const box = el("div", { className: "actions" });
  if (!v.actions.length) {
    box.append(el("p", { className: "meta", textContent: "This alert is finished. No further actions." }));
    return box;
  }
  const qty = el("input", { type: "number", min: 1, max: v.lot.quantity, value: v.lot.quantity, id: "f-qty" });
  const recips = el("input", { placeholder: "Partner agencies that received this lot, comma separated", id: "f-recips" });
  const note = el("textarea", { rows: 2, placeholder: "Note (required to dismiss, or to acknowledge a review-tier match)", id: "f-note" });
  box.append(
    el("label", {}, "Quantity to pull ", qty),
    el("label", {}, "Notified ", recips),
    note,
  );
  const btns = el("div", { className: "btns" });
  for (const action of v.actions) {
    const b = el("button", { type: "button", textContent: ACTION_LABELS[action] || action });
    if (action !== "dismiss") b.className = "primary";
    b.addEventListener("click", () => submit(v, action, { qty, recips, note }, b));
    btns.append(b);
  }
  box.append(btns);
  return box;
}

async function submit(v, action, f, button) {
  const actor = actorInput.value.trim();
  if (!actor) {
    toast("Enter your name at the top first: every action is recorded.", true);
    actorInput.focus();
    return;
  }
  const cmd = { version: v.version, action, actor, note: f.note.value.trim() };
  if (action === "pull") cmd.quantity = Number(f.qty.value);
  if (action === "notify") cmd.recipients = f.recips.value.split(",").map((s) => s.trim()).filter(Boolean);
  button.disabled = true;
  try {
    await api("POST", `/api/alerts/${v.id}/transitions`, cmd);
    toast(`${ACTION_LABELS[action]}: done`);
    await openDetail(v.id);
  } catch (e) {
    if (e.code === "version_conflict") {
      toast("Someone else just updated this alert. Showing the latest version.", true);
      await openDetail(v.id);
    } else {
      toast(e.message, true);
    }
  } finally {
    button.disabled = false;
  }
}

function timeline(events) {
  const ul = el("ul", { className: "timeline" });
  if (!events.length) ul.append(el("li", { textContent: "No actions yet." }));
  for (const e of events) {
    const parts = [`${e.actor}: ${e.action}`, `(${e.from} → ${e.to})`];
    if (e.quantity) parts.push(`${e.quantity} units`);
    if (e.recipients && e.recipients.length) parts.push(`notified ${e.recipients.join(", ")}`);
    if (e.note) parts.push(`“${e.note}”`);
    ul.append(el("li", {}, el("time", { textContent: new Date(e.at).toLocaleString() }), parts.join(" ")));
  }
  return ul;
}

// ---------- imports ----------

$("#csv").addEventListener("change", async (ev) => {
  const file = ev.target.files[0];
  if (!file) return;
  try {
    const r = await api("POST", "/api/lots", await file.text(), "text/csv");
    const errs = r.row_errors.length ? ` · ${r.row_errors.length} rows skipped (first: line ${r.row_errors[0].line}: ${r.row_errors[0].message})` : "";
    toast(`Imported ${r.imported} lots, ${r.new_alerts.length} new alerts${errs}`, r.row_errors.length > 0);
  } catch (e) {
    toast("Import failed: " + e.message, true);
  }
  ev.target.value = "";
});

$("#demo-lots").addEventListener("click", async () => {
  try {
    const csv = await (await fetch("demo/inventory_sample.csv")).text();
    const r = await api("POST", "/api/lots", csv, "text/csv");
    toast(`Imported ${r.imported} demo lots, ${r.new_alerts.length} new alerts`);
  } catch (e) { toast(e.message, true); }
});

$("#demo-recalls").addEventListener("click", async () => {
  try {
    const feed = await (await fetch("demo/openfda_sample.json")).text();
    const r = await api("POST", "/api/recalls/ingest", feed);
    toast(`Ingested ${r.received} recalls (${r.changed} new or changed), ${r.new_alerts.length} new alerts`);
  } catch (e) { toast(e.message, true); }
});

$("#poll").addEventListener("click", async () => {
  try {
    const r = await api("POST", "/api/recalls/poll");
    toast(`Feed checked: ${r.received} recalls, ${r.new_alerts.length} new alerts`);
  } catch (e) { toast(e.message, true); }
});

// ---------- live updates ----------

let retry = 0;
let refreshQueued = false;

function queueRefresh() {
  // Coalesce bursts (an ingest can create dozens of alerts at once).
  if (refreshQueued) return;
  refreshQueued = true;
  setTimeout(() => { refreshQueued = false; refresh(); }, 150);
}

function connect() {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.onopen = () => {
    retry = 0;
    $("#ws-dot").className = "dot on";
    $("#ws-label").textContent = "live";
    refresh(); // catch up on anything missed while disconnected
  };
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.type === "hello") return;
    queueRefresh();
    const dlg = $("#detail");
    // If someone else moved the alert that is open here, show their change.
    if (msg.type === "alert.updated" && dlg.open && msg.data.alert && dlg.dataset.id === String(msg.data.alert.id)) {
      openDetail(msg.data.alert.id);
    }
  };
  ws.onclose = () => {
    $("#ws-dot").className = "dot off";
    $("#ws-label").textContent = "reconnecting…";
    const delay = Math.min(30000, 1000 * 2 ** retry++);
    setTimeout(connect, delay);
  };
}

(async function init() {
  try {
    const h = await api("GET", "/healthz");
    $(".sub").textContent = `recall pull board · ${h.store} store`;
    $("#poll").hidden = !h.poll;
  } catch { /* shown by refresh */ }
  refresh();
  connect();
})();
