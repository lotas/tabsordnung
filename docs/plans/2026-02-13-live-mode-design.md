# Live Mode & WebExtension Companion — Design Document

## Overview

Tabsordnung gains a companion WebExtension and a WebSocket bridge between the TUI and Firefox. This enables live tab data and actions (close, focus, move) from within the TUI.

## Modes

Two explicit operating modes, selectable via a numbered source picker:

- **`1` — Live mode**: Starts a WebSocket server, waits for the extension to connect. Tab data comes from the extension with real Firefox tab IDs. Actions are available.
- **`2+` — Offline profiles**: Discovered from `profiles.ini`. Session file reading, analytics only, no actions.

Number keys (`1`-`9`) fast-switch from anywhere in the TUI. The `--live` CLI flag starts directly in live mode. The `--profile` flag selects an offline profile. A `--port` flag overrides the default WebSocket port.

```
┌─ Select source ──────────────────────┐
│                                      │
│  1  ● Live (connected)               │
│  2    default-release                │
│  3    dev-profile                    │
│                                      │
│  ↑↓ navigate · enter select · 1-9   │
└──────────────────────────────────────┘
```

Status bar reflects current source: `Live ● connected` or `Profile: default-release (offline)`.

## Architecture

```
┌─────────────┐  WebSocket   ┌──────────────────────┐
│ Tabsordnung │◄────────────►│  WebExtension        │
│ TUI (Go)    │  localhost    │  (browser.tabs)      │
│             │              │  (browser.tabGroups)  │
└─────────────┘              └──────────────────────┘
      │
      ▼ (offline mode)
  session files
```

The TUI starts a WebSocket server on a fixed port (default `19191`). The extension connects on load. Communication is bidirectional JSON.

## WebSocket Protocol

### Extension → TUI (data sync)

On connect, the extension sends a full snapshot. After that, incremental updates on tab/group changes.

```json
{"type": "snapshot", "tabs": [...], "groups": [...]}
{"type": "tab.created", "tab": {...}}
{"type": "tab.removed", "tabId": 42}
{"type": "tab.updated", "tab": {...}}
{"type": "tab.moved", "tab": {...}}
{"type": "group.created", "group": {...}}
{"type": "group.updated", "group": {...}}
```

### TUI → Extension (commands)

```json
{"id": "cmd-1", "action": "close", "tabIds": [42, 43]}
{"id": "cmd-2", "action": "focus", "tabId": 42}
{"id": "cmd-3", "action": "move", "tabIds": [42], "groupId": 5}
```

### Extension → TUI (command responses)

```json
{"id": "cmd-1", "ok": true}
{"id": "cmd-2", "ok": false, "error": "tab not found"}
```

Commands have an `id` so the TUI can match responses. Port default: `19191`, overridable with `--port`.

## WebExtension

Minimal extension — a thin bridge between the WebSocket and Firefox tab APIs.

### Manifest (v3)

```json
{
  "manifest_version": 3,
  "name": "Tabsordnung Companion",
  "permissions": ["tabs", "tabGroups"],
  "background": {
    "scripts": ["background.js"]
  }
}
```

No content scripts, no popups, no UI. Background script only.

### Background script responsibilities

1. **Connect** — On startup, connect to `ws://localhost:19191`. Reconnect with backoff if the TUI isn't running yet.
2. **Send snapshot** — On connect, query all tabs and groups via `browser.tabs.query({})` and `browser.tabGroups.query({})`, send as `snapshot` message.
3. **Stream updates** — Listen to `browser.tabs.onCreated`, `onRemoved`, `onUpdated`, `onMoved`, and `browser.tabGroups.onCreated`, `onUpdated`. Forward as incremental events.
4. **Execute commands** — On incoming `close`/`focus`/`move` messages, call the corresponding `browser.tabs.*` API and send back a response with the command `id`.

### Tab data sent to TUI

`id`, `url`, `title`, `lastAccessed`, `groupId`, `windowId`, `index`, `favIconUrl`, `active`.

### Note on tab groups

`browser.tabGroups` is a Chrome-origin API. Firefox's tab group support varies by version. The extension should degrade gracefully if the API is unavailable.

### Distribution

Load as temporary add-on via `about:debugging` during development. Package as `.xpi` for sideloading. Publish to AMO once stable.

## TUI Changes

### New keybindings (live mode only)

| Key | Action |
|-----|--------|
| `Space` | Toggle select on current tab |
| `x` | Close selected tabs (or current tab if none selected) |
| `Enter` | Focus tab in browser (live) / toggle expand (offline) |
| `g` | Move selected tabs to a group (opens group picker) |
| `Esc` | Clear selection |
| `1`-`9` | Switch source (replaces `p`) |

In offline mode, `x`/`g`/`Space` do nothing. Bottom bar shows only available actions for current mode.

### Selection

Selected tabs get a visual marker. Selection count in status bar: `3 selected`. Tree model gets a `Selected` map keyed by tab ID (like `Expanded` map for groups).

### Group picker overlay

Pressing `g` opens an overlay listing available groups. Select destination and confirm.

### Connection status

```
Live ● connected │ 147 tabs · 12 groups · 3 dead · 8 stale
```
```
Live ○ waiting... │ no data
```

On disconnect: actions disabled, existing data stays visible, automatic reconnection.

## Data Flow & Analyzer Integration

Analyzers operate on `SessionData` regardless of source:

```
Source (extension OR session file)
    ↓
Convert to SessionData
    ↓
Run analyzers (stale, duplicates, dead links)
    ↓
Render TUI
```

In connected mode, the `snapshot` message maps to `SessionData`. Incremental updates mutate in place and re-run fast analyzers. Dead link checks run once on connect, then only for new tabs.

### Re-analysis triggers

| Event | Action |
|-------|--------|
| Snapshot received | Full analysis (stale + duplicates + dead links) |
| Tab created | Analyze new tab (stale + duplicate + dead link) |
| Tab removed | Remove from data, recalculate duplicates |
| Tab updated (URL change) | Re-analyze that tab |
| Manual `r` reload | Live: request fresh snapshot. Offline: re-read session file. |

### Type change

`types.Tab` gains `BrowserID int` — the live Firefox tab ID, zero in offline mode. Action commands reference this ID.

## Project Structure

```
tabsordnung/
├── internal/
│   ├── server/
│   │   └── websocket.go      # WebSocket server, message types, send/receive
│   ├── types/
│   │   └── types.go           # Add BrowserID, message types
│   ├── tui/
│   │   ├── app.go             # Source switching, connection state, selection
│   │   ├── tree.go            # Selection markers, action keybindings
│   │   ├── source_picker.go   # Replaces profile_picker.go
│   │   └── group_picker.go    # Move-to-group overlay
│   └── ...                    # firefox/, analyzer/ unchanged
├── extension/
│   ├── manifest.json
│   └── background.js
├── main.go                    # Add --live, --port flags
└── ...
```

### New Go dependency

`nhooyr.io/websocket` for the WebSocket server.

### Extension directory

`extension/` at repo root. Plain JS, no build step.
