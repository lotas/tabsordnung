const PORT = 19191;
const RECONNECT_BASE_MS = 1000;
const ALARM_NAME = "keepalive";
const RECONNECT_MAX_MS = 30000;

let ws = null;
let reconnectDelay = RECONNECT_BASE_MS;

function connect() {
  ws = new WebSocket(`ws://127.0.0.1:${PORT}`);

  ws.addEventListener("open", async () => {
    console.log("Tabsordnung: connected");
    reconnectDelay = RECONNECT_BASE_MS;
    await sendSnapshot();
  });

  ws.addEventListener("message", (event) => {
    handleCommand(JSON.parse(event.data));
  });

  ws.addEventListener("close", () => {
    console.log("Tabsordnung: disconnected, reconnecting...");
    ws = null;
    scheduleReconnect();
  });

  ws.addEventListener("error", () => {
    ws?.close();
  });
}

function scheduleReconnect() {
  setTimeout(() => {
    connect();
    reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_MS);
  }, reconnectDelay);
}

function ensureConnected() {
  if (!ws || ws.readyState === WebSocket.CLOSED || ws.readyState === WebSocket.CLOSING) {
    reconnectDelay = RECONNECT_BASE_MS;
    connect();
  }
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
              return { title: sender, preview: subject };
            });
          },
          slack: () => {
            const unreads = document.querySelectorAll(".p-channel_sidebar__link--unread .p-channel_sidebar__name");
            if (unreads.length > 0) {
              return Array.from(unreads).slice(0, 20).map(el => ({
                title: el.textContent?.trim() || "",
                preview: "unread channel",
              }));
            }
            const msgs = document.querySelectorAll("[data-qa='virtual-list-item'] .c-message_kit__text");
            return Array.from(msgs).slice(-20).map(el => ({
              title: "",
              preview: el.textContent?.trim() || "",
            }));
          },
          matrix: () => {
            const badges = document.querySelectorAll(".mx_RoomTile_badge, .mx_NotificationBadge");
            const rooms = document.querySelectorAll(".mx_RoomTile");
            const items = [];
            rooms.forEach(room => {
              const badge = room.querySelector(".mx_RoomTile_badge, .mx_NotificationBadge");
              if (badge && badge.textContent?.trim() !== "0") {
                const name = room.querySelector(".mx_RoomTile_title")?.textContent?.trim() || "";
                items.push({ title: name, preview: badge.textContent?.trim() + " unread" });
              }
            });
            return items.length > 0 ? items : Array.from(badges).slice(0, 20).map(b => ({
              title: "",
              preview: b.textContent?.trim() + " notifications",
            }));
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

// --- Start ---

browser.alarms.create(ALARM_NAME, { periodInMinutes: 0.5 });

browser.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === ALARM_NAME) {
    ensureConnected();
  }
});

connect();
