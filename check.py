import json, lz4.block, sys

# Read the mozlz4 file
with open('/Users/ykurmyza/Library/Application Support/Firefox/Profiles/l32aussE.Profile 1/sessionstore-backups/recovery.jsonlz4', 'rb') as f:
   data = f.read()

# Skip 8-byte magic, 4-byte size header
raw = lz4.block.decompress(data[12:], uncompressed_size=int.from_bytes(data[8:12], 'little'))
session = json.loads(raw)

# Show the structure of the first window's groups and first few tabs
for i, win in enumerate(session.get('windows', [])):
   print(f'=== Window {i} ===')
   print(f'Groups: {json.dumps(win.get("groups", []), indent=2)[:2000]}')
   print()
   for j, tab in enumerate(win.get('tabs', [])[:5]):
	   entry = tab['entries'][tab['index']-1] if tab.get('entries') else {}
	   print(f'Tab {j}: group={tab.get("group", "MISSING")!r}, url={entry.get("url", "")[:80]}')
   print(f'... total {len(win.get("tabs", []))} tabs')

