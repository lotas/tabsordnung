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

init();
