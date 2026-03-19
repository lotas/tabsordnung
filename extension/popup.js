const $ = (sel) => document.querySelector(sel);

async function init() {
  let response;
  try {
    response = await browser.runtime.sendMessage({ action: "get-tab-info" });
  } catch (e) {
    showDisconnected();
    return;
  }

  if (!response || !response.connected) {
    showDisconnected();
    return;
  }

  renderTabInfo(response);

  // Detect Slack thread if on a Slack tab
  if (response.url && response.url.includes("slack.com")) {
    detectThread();
  }
}

function showDisconnected() {
  $("#loading").classList.add("hidden");
  $("#disconnected").classList.remove("hidden");
}

function renderTabInfo(data) {
  $("#loading").classList.add("hidden");
  $("#tab-info").classList.remove("hidden");

  $("#tab-title").textContent = data.title || "(untitled)";
  $("#tab-url").textContent = data.url || "";

  // Meta line: last accessed + stale days
  const parts = [];
  if (data.lastAccessed) {
    parts.push("Last accessed: " + data.lastAccessed);
  }
  if (data.staleDays > 0) {
    parts.push(data.staleDays + " days old");
  }
  $("#tab-meta").textContent = parts.join(" · ");

  // Badges
  const badges = $("#tab-badges");
  badges.innerHTML = "";
  if (data.isStale) {
    badges.appendChild(makeBadge("stale", "Stale"));
  }
  if (data.isDead) {
    const reason = data.deadReason ? `Dead (${data.deadReason})` : "Dead";
    badges.appendChild(makeBadge("dead", reason));
  }
  if (data.isDuplicate) {
    badges.appendChild(makeBadge("duplicate", "Duplicate"));
  }
  if (data.githubStatus) {
    const cls = "github " + data.githubStatus;
    badges.appendChild(makeBadge(cls, "GH: " + data.githubStatus));
  }

  // Summary
  if (data.summary) {
    $("#summary-content").innerHTML = marked.parse(data.summary);
    $("#summary-content").classList.remove("hidden");
  } else {
    $("#summary-none").classList.remove("hidden");
  }
  $("#summarize-btn").classList.remove("hidden");
  $("#summarize-btn").addEventListener("click", () => summarizeTab());

  // Notes
  renderNotes(data.notes || []);

  // Signals
  if (data.signalSource && data.signals && data.signals.length > 0) {
    $("#signals-section").classList.remove("hidden");
    $("#signal-source-label").textContent = `(${data.signalSource})`;
    const list = $("#signals-list");
    for (const sig of data.signals) {
      const li = document.createElement("li");
      const title = document.createElement("span");
      title.className = "signal-title";
      title.textContent = sig.title;
      li.appendChild(title);
      if (sig.preview) {
        const preview = document.createElement("div");
        preview.className = "signal-preview";
        preview.textContent = sig.preview;
        li.appendChild(preview);
      }
      if (sig.sourceTs) {
        const ts = document.createElement("div");
        ts.className = "signal-ts";
        ts.textContent = sig.sourceTs;
        li.appendChild(ts);
      }
      list.appendChild(li);
    }
  }
}

function makeBadge(cls, text) {
  const span = document.createElement("span");
  span.className = "badge " + cls;
  span.textContent = text;
  return span;
}

async function summarizeTab() {
  const btn = $("#summarize-btn");
  btn.classList.add("hidden");
  $("#summary-none").classList.add("hidden");
  $("#summary-spinner").classList.remove("hidden");
  $("#summary-error").classList.add("hidden");

  let response;
  try {
    response = await browser.runtime.sendMessage({ action: "summarize-tab" });
  } catch (e) {
    $("#summary-spinner").classList.add("hidden");
    $("#summary-error").textContent = "Failed: " + e.message;
    $("#summary-error").classList.remove("hidden");
    btn.classList.remove("hidden");
    return;
  }

  $("#summary-spinner").classList.add("hidden");

  if (response && response.summary) {
    $("#summary-content").innerHTML = marked.parse(response.summary);
    $("#summary-content").classList.remove("hidden");
    btn.classList.remove("hidden");
  } else {
    const errMsg = (response && response.error) || "Summarization failed";
    $("#summary-error").textContent = errMsg;
    $("#summary-error").classList.remove("hidden");
    btn.classList.remove("hidden");
  }
}

let currentThread = null;

async function detectThread() {
  let response;
  try {
    response = await browser.runtime.sendMessage({ action: "detect-thread" });
  } catch (e) {
    return;
  }

  if (!response || !response.thread) return;

  currentThread = response.thread;
  $("#thread-section").classList.remove("hidden");

  if (response.thread.summary) {
    $("#thread-summary-content").innerHTML = marked.parse(response.thread.summary);
    $("#thread-summary-content").classList.remove("hidden");
    $("#resummarize-thread-btn").classList.remove("hidden");
    $("#resummarize-thread-btn").addEventListener("click", () => summarizeThread());
  } else {
    $("#thread-summary-none").classList.remove("hidden");
    $("#summarize-thread-btn").classList.remove("hidden");
    $("#summarize-thread-btn").addEventListener("click", () => summarizeThread());
  }
}

async function summarizeThread() {
  if (!currentThread) return;

  $("#summarize-thread-btn").classList.add("hidden");
  $("#resummarize-thread-btn").classList.add("hidden");
  $("#thread-summary-none").classList.add("hidden");
  $("#thread-summary-error").classList.add("hidden");
  $("#thread-summary-content").classList.add("hidden");
  $("#thread-summary-spinner").classList.remove("hidden");

  let response;
  try {
    response = await browser.runtime.sendMessage({
      action: "summarize-thread",
      channelId: currentThread.channelId,
      threadTs: currentThread.threadTs,
    });
  } catch (e) {
    $("#thread-summary-spinner").classList.add("hidden");
    $("#thread-summary-error").textContent = "Failed: " + e.message;
    $("#thread-summary-error").classList.remove("hidden");
    $("#summarize-thread-btn").classList.remove("hidden");
    return;
  }

  $("#thread-summary-spinner").classList.add("hidden");

  if (response && response.summary) {
    $("#thread-summary-content").innerHTML = marked.parse(response.summary);
    $("#thread-summary-content").classList.remove("hidden");
    $("#resummarize-thread-btn").classList.remove("hidden");
  } else {
    const errMsg = (response && response.error) || "Thread summarization failed";
    $("#thread-summary-error").textContent = errMsg;
    $("#thread-summary-error").classList.remove("hidden");
    $("#summarize-thread-btn").classList.remove("hidden");
  }
}

function makeNoteMeta(ageText, sourceText) {
  const meta = document.createElement("div");
  meta.className = "note-meta";
  meta.appendChild(document.createTextNode(ageText + " · "));
  const sourceSpan = document.createElement("span");
  sourceSpan.className = "note-source";
  sourceSpan.textContent = sourceText;
  meta.appendChild(sourceSpan);
  return meta;
}

function renderNotes(notes) {
  const section = $("#notes-section");
  section.classList.remove("hidden");
  const list = $("#notes-list");
  list.innerHTML = "";

  for (const note of notes) {
    const li = document.createElement("li");
    const body = document.createElement("div");
    body.className = "note-body";
    body.textContent = note.body;
    li.appendChild(body);
    li.appendChild(makeNoteMeta(formatAge(note.createdAt), note.source));
    list.appendChild(li);
  }

  // Wire up buttons only once; replace elements to remove old listeners
  const addBtn = $("#add-note-btn");
  const newAddBtn = addBtn.cloneNode(true);
  addBtn.parentNode.replaceChild(newAddBtn, addBtn);

  const cancelBtn = $("#note-cancel-btn");
  const newCancelBtn = cancelBtn.cloneNode(true);
  cancelBtn.parentNode.replaceChild(newCancelBtn, cancelBtn);

  const submitBtn = $("#note-submit-btn");
  const newSubmitBtn = submitBtn.cloneNode(true);
  submitBtn.parentNode.replaceChild(newSubmitBtn, submitBtn);

  const inputContainer = $("#note-input-container");
  const input = $("#note-input");

  newAddBtn.addEventListener("click", () => {
    newAddBtn.classList.add("hidden");
    inputContainer.classList.remove("hidden");
    input.focus();
  });

  newCancelBtn.addEventListener("click", () => {
    input.value = "";
    inputContainer.classList.add("hidden");
    newAddBtn.classList.remove("hidden");
  });

  newSubmitBtn.addEventListener("click", async () => {
    const body = input.value.trim();
    if (!body) return;
    newSubmitBtn.disabled = true;
    const response = await browser.runtime.sendMessage({ action: "create-note", body });
    newSubmitBtn.disabled = false;
    if (response && response.ok) {
      input.value = "";
      inputContainer.classList.add("hidden");
      newAddBtn.classList.remove("hidden");
      // notes-updated push from TUI will re-render the list
    }
  });
}

function formatAge(isoDate) {
  if (!isoDate) return "";
  const diff = Date.now() - new Date(isoDate).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 60) return mins + "m ago";
  const hours = Math.floor(mins / 60);
  if (hours < 24) return hours + "h ago";
  const days = Math.floor(hours / 24);
  return days + "d ago";
}

browser.runtime.onMessage.addListener((message) => {
  if (message.action === "notes-updated" && message.notes) {
    renderNotes(message.notes);
  }
  if (message.action === "summary-ready" && message.summary) {
    $("#summary-spinner").classList.add("hidden");
    $("#summary-none").classList.add("hidden");
    $("#summary-error").classList.add("hidden");
    $("#summary-content").innerHTML = marked.parse(message.summary);
    $("#summary-content").classList.remove("hidden");
    $("#summarize-btn").classList.remove("hidden");
  }
});

init();
