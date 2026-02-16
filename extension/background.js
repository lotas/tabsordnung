const PORT = 19191;
const RECONNECT_BASE_MS = 1000;
const ALARM_NAME = "keepalive";
const RECONNECT_MAX_MS = 30000;

let ws = null;
let reconnectDelay = RECONNECT_BASE_MS;
let reconnectTimer = null;
let pendingPopupRequests = new Map(); // id → {resolve, reject}
let popupCmdCounter = 0;

function connect() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }

  const socket = new WebSocket(`ws://127.0.0.1:${PORT}`);
  ws = socket;

  socket.addEventListener("open", async () => {
    console.log("Tabsordnung: connected");
    reconnectDelay = RECONNECT_BASE_MS;
    browser.action.setIcon({ path: { "32": "icons/icon-32.svg" } });
    await sendSnapshot();
  });

  socket.addEventListener("message", (event) => {
    const msg = JSON.parse(event.data);
    // Route responses to pending popup requests before handleCommand
    if (msg.id && pendingPopupRequests.has(msg.id)) {
      const pending = pendingPopupRequests.get(msg.id);
      pendingPopupRequests.delete(msg.id);
      pending.resolve(msg);
      return;
    }
    handleCommand(msg);
  });

  socket.addEventListener("close", () => {
    if (ws !== socket) return; // stale close — ignore
    console.log("Tabsordnung: disconnected, reconnecting...");
    ws = null;
    browser.action.setIcon({ path: { "32": "icons/icon-grey-32.svg" } });
    // Reject all pending popup requests
    for (const [id, pending] of pendingPopupRequests) {
      pending.resolve({ connected: false });
    }
    pendingPopupRequests.clear();
    scheduleReconnect();
  });

  socket.addEventListener("error", () => {
    socket.close();
  });
}

function scheduleReconnect() {
  if (reconnectTimer) return; // already scheduled
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    connect();
    reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_MS);
  }, reconnectDelay);
}

function ensureConnected() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
    return; // already connected or connecting
  }
  if (reconnectTimer) {
    return; // reconnect already scheduled
  }
  reconnectDelay = RECONNECT_BASE_MS;
  connect();
}

function send(obj) {
  if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(obj));
  }
}

// --- Snapshot ---

async function sendSnapshot() {
  const tabs = await browser.tabs.query({});
  let groups = [];
  if (browser.tabGroups?.query) {
    groups = await browser.tabGroups.query({});
  }

  send({
    type: "snapshot",
    tabs: tabs.map(serializeTab),
    groups: groups.map(serializeGroup),
  });
}

function serializeTab(tab) {
  return {
    id: tab.id,
    url: tab.url || "",
    title: tab.title || "",
    lastAccessed: tab.lastAccessed || 0,
    groupId: tab.groupId ?? -1,
    windowId: tab.windowId,
    index: tab.index,
    favIconUrl: tab.favIconUrl || "",
  };
}

function serializeGroup(group) {
  return {
    id: group.id,
    title: group.title || "",
    color: group.color || "",
    collapsed: group.collapsed || false,
  };
}

// --- Events ---

browser.tabs.onCreated.addListener((tab) => {
  ensureConnected();
  send({ type: "tab.created", tab: serializeTab(tab) });
});

browser.tabs.onRemoved.addListener((tabId) => {
  ensureConnected();
  send({ type: "tab.removed", tabId });
});

browser.tabs.onUpdated.addListener((_tabId, _changeInfo, tab) => {
  ensureConnected();
  send({ type: "tab.updated", tab: serializeTab(tab) });
});

browser.tabs.onMoved.addListener(async (tabId) => {
  ensureConnected();
  const tab = await browser.tabs.get(tabId);
  send({ type: "tab.moved", tab: serializeTab(tab) });
});

// --- Commands ---

async function handleCommand(msg) {
  try {
    switch (msg.action) {
      case "close":
        await browser.tabs.remove(msg.tabIds);
        break;
      case "focus":
        await browser.tabs.update(msg.tabId, { active: true });
        const tab = await browser.tabs.get(msg.tabId);
        await browser.windows.update(tab.windowId, { focused: true });
        break;
      case "get-content": {
        const results = await browser.scripting.executeScript({
          target: { tabId: msg.tabId },
          func: () => document.body?.innerText || "",
        });
        send({ id: msg.id, ok: true, content: results?.[0]?.result || "" });
        return;
      }
      case "move":
        if (browser.tabs.group) {
          await browser.tabs.group({ tabIds: msg.tabIds, groupId: msg.groupId });
        }
        break;
      case "open":
        for (const tab of (msg.tabs || [])) {
          await browser.tabs.create({
            url: tab.url,
            pinned: tab.pinned || false,
          });
        }
        break;
      case "create-group":
        if (browser.tabs.group) {
          const tabIds = msg.tabIds || [];
          if (tabIds.length === 0) {
            send({ id: msg.id, ok: true, groupId: -1 });
            return;
          }
          const groupId = await chrome.tabs.group({ tabIds });
          await chrome.tabGroups.update(groupId, {
            title: msg.name || "",
            color: msg.color || "blue",
          });
          send({ id: msg.id, ok: true, groupId });
          return;
        }
        // Firefox doesn't have native tab groups API yet
        send({ id: msg.id, ok: true, groupId: -1 });
        return;
      case "scrape-activity": {
        const scrapers = {
          gmail: () => {
            const rows = document.querySelectorAll("tr.zE");
            return Array.from(rows).slice(0, 20).map(row => {
              const sender = row.querySelector(".yX.yW span")?.getAttribute("name") || row.querySelector(".yX.yW")?.textContent?.trim() || "";
              const subject = row.querySelector(".bog span")?.textContent?.trim() || row.querySelector(".y6 span")?.textContent?.trim() || "";
              const timestamp = row.querySelector("td.xW span")?.getAttribute("title") || row.querySelector("td.xW span")?.textContent?.trim() || "";
              return { title: sender, preview: subject, timestamp };
            });
          },
          slack: () => {
            const channels = document.querySelectorAll(".p-channel_sidebar__channel--unread");
            return Array.from(channels).slice(0, 20).map(el => {
              const name = el.querySelector(".p-channel_sidebar__name")?.textContent?.trim() || "";
              const badge = el.querySelector('[data-qa="mention_badge"]')?.textContent?.trim() || "";
              const type = el.getAttribute("data-qa-channel-sidebar-channel-type") === "im" ? "dm" : "channel";
              const parts = [];
              if (type === "dm") parts.push("DM");
              if (badge) parts.push(`${badge} mentioned`);
              else parts.push("unread");
              return { title: name, preview: parts.join(" · "), timestamp: "" };
            });
          },
          matrix: () => {
            const rooms = document.querySelectorAll(".mx_RoomTile");
            const items = [];
            rooms.forEach(room => {
              const badge = room.querySelector(".mx_RoomTile_badge, .mx_NotificationBadge");
              if (badge && badge.textContent?.trim() !== "0") {
                const name = room.querySelector(".mx_RoomTile_title")?.textContent?.trim() || "";
                items.push({ title: name, preview: badge.textContent?.trim() + " unread", timestamp: "" });
              }
            });
            return items;
          },
        };

        const scraper = scrapers[msg.source];
        if (!scraper) {
          send({ id: msg.id, ok: false, error: `unknown source: ${msg.source}` });
          return;
        }

        const results = await browser.scripting.executeScript({
          target: { tabId: msg.tabId },
          func: scraper,
        });

        const items = results?.[0]?.result || [];
        send({ id: msg.id, ok: true, items: JSON.stringify(items), source: msg.source });
        return;
      }
      default:
        send({ id: msg.id, ok: false, error: `unknown action: ${msg.action}` });
        return;
    }
    send({ id: msg.id, ok: true });
  } catch (e) {
    send({ id: msg.id, ok: false, error: e.message });
  }
}

// --- Popup communication ---

function nextPopupCmdID() {
  return `popup-${++popupCmdCounter}`;
}

browser.runtime.onMessage.addListener((message, _sender) => {
  if (message.action === "get-tab-info") {
    return handleGetTabInfo();
  }
  if (message.action === "summarize-tab") {
    return handleSummarizeTab();
  }
  return false;
});

async function handleGetTabInfo() {
  if (!ws || ws.readyState !== WebSocket.OPEN) {
    return { connected: false };
  }

  const [activeTab] = await browser.tabs.query({ active: true, currentWindow: true });
  if (!activeTab) {
    return { connected: true, error: "No active tab" };
  }

  const id = nextPopupCmdID();
  send({ type: "get-tab-info", id, tabId: activeTab.id });

  return new Promise((resolve) => {
    const timeout = setTimeout(() => {
      pendingPopupRequests.delete(id);
      resolve({ connected: true, error: "Timeout" });
    }, 10000);

    pendingPopupRequests.set(id, {
      resolve: (msg) => {
        clearTimeout(timeout);
        resolve({ connected: true, ...msg.tabInfo });
      },
    });
  });
}

async function handleSummarizeTab() {
  if (!ws || ws.readyState !== WebSocket.OPEN) {
    return { connected: false };
  }

  const [activeTab] = await browser.tabs.query({ active: true, currentWindow: true });
  if (!activeTab) {
    return { connected: true, error: "No active tab" };
  }

  const id = nextPopupCmdID();
  send({ type: "summarize-tab", id, tabId: activeTab.id });

  return new Promise((resolve) => {
    const timeout = setTimeout(() => {
      pendingPopupRequests.delete(id);
      resolve({ error: "Summarization timed out" });
    }, 120000); // 2 min for summarization

    pendingPopupRequests.set(id, {
      resolve: (msg) => {
        clearTimeout(timeout);
        if (msg.error) {
          resolve({ error: msg.error });
        } else {
          resolve({ summary: msg.summary });
        }
      },
    });
  });
}

// --- Start ---

browser.alarms.create(ALARM_NAME, { periodInMinutes: 0.5 });

browser.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === ALARM_NAME) {
    ensureConnected();
  }
});

connect();
