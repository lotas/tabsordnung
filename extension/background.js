const PORT = 19191;
const RECONNECT_BASE_MS = 1000;
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
  send({ type: "tab.created", tab: serializeTab(tab) });
});

browser.tabs.onRemoved.addListener((tabId) => {
  send({ type: "tab.removed", tabId });
});

browser.tabs.onUpdated.addListener((_tabId, _changeInfo, tab) => {
  send({ type: "tab.updated", tab: serializeTab(tab) });
});

browser.tabs.onMoved.addListener(async (tabId) => {
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

connect();
