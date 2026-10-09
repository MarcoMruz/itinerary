# Rodinné itineráre

Agent-ready web app for family travel itineraries. Go (stdlib only, zero dependencies) + Alpine.js + Tailwind (CDN).
Data lives in memory and is written atomically to `data/itineraries.json` on every change.

## Run

```sh
go run .                       # http://localhost:9876
go test ./...
docker build -t itinerary . && docker run -p 9876:9876 -v itinerary-data:/root/data itinerary
```

| Env                     | Default                 | Purpose                                                                 |
|-------------------------|-------------------------|-------------------------------------------------------------------------|
| `PORT`                  | `9876`                  | Listen port                                                             |
| `DATA_FILE`             | `data/itineraries.json` | JSON storage file (seeded with the default itinerary if missing)        |
| `API_TOKEN`             | *(empty = open)*        | Bearer token for everything: writes, REST reads and `/mcp`              |
| `READ_API_TOKEN`        | *(empty)*               | Extra read-only bearer token (REST reads, MCP reads). Set alone, writes are disabled |
| `PUBLIC_URL`            | *(from request)*        | Absolute base URL placed in `openapi.json` `servers`                     |
| `CORS_ORIGINS`          | *(empty = same-origin)* | Comma-separated origins allowed to call `/api/*` and `/mcp` from a browser |
| `RATE_LIMIT_PER_MINUTE` | `120`                   | Requests per minute per client IP (`0` disables); over the limit → `429` + `Retry-After` |
| `RATE_LIMIT_BURST`      | `30`                    | Requests a client can make at once before the per-minute rate applies   |
| `CLIENT_IP_HEADER`      | *(empty = peer IP)*     | Header holding the real client IP, e.g. `CF-Connecting-IP` behind Cloudflare |
| `BLOCK_BOTS`            | `true`                  | `403` for empty user agents, AI/SEO crawlers and scanners (`false` to disable) |
| `DISTANCES`             | `true`                  | Driving distance between route stops (`false` to disable; place names are then never sent to OpenStreetMap) |
| `START_LOOKUPS_PER_MINUTE` | `20`                 | Uncached lookups (geocode, start→first stop) one client's start may cause per minute, burst half of it; all clients together are capped at 30/min so stops always get through (`0` disables both). Keyed by client IP, so set `CLIENT_IP_HEADER` behind a proxy |

**Coolify:** use the Dockerfile build pack, port `9876`, add a persistent storage volume mounted at `/root/data`,
and set `API_TOKEN` (otherwise anyone who can reach the app can read, add or delete itineraries).

## Security

- **Access:** once a token is set, `/api/v1/itineraries*` and `/mcp` need `Authorization: Bearer <token>`.
  The web page sets an `HttpOnly`, `SameSite=Strict` read-only cookie so the UI keeps working; it cannot write.
  The page itself stays public, so put Cloudflare Access in front of `/` if the content must be private.
  `openapi.json`, `robots.txt` and `/healthz` stay public.
- **Cache-Control (Cloudflare):** API, MCP and error responses are `no-store`; the page is `private, no-cache`
  (it sets the cookie); `openapi.json` is `public, max-age=300`; `robots.txt` is `public, max-age=86400`.
- **Bots:** `robots.txt` disallows everything and every response sends `X-Robots-Tag: noindex, nofollow`.

Cloudflare setup:

- Set `CLIENT_IP_HEADER=CF-Connecting-IP` only if the origin is reachable through Cloudflare alone;
  otherwise clients can spoof the header and dodge the rate limit.
- Turn on Bot Fight Mode and "Block AI bots" (Security → Bots).
- Add a WAF rate-limiting rule on `/api/` and `/mcp` as a first line in front of the app limit.
- Leave the default cache level; nothing here needs a Cache Rule.

## Agent interfaces

- REST: `GET|POST /api/v1/itineraries`, `GET|PATCH|DELETE /api/v1/itineraries/{id}`
- OpenAPI 3.1: `GET /api/v1/openapi.json` (GPT Actions, custom tools)
- MCP: `POST /mcp` (JSON-RPC 2.0, Streamable HTTP with JSON responses). Tools: `list_itineraries`, `get_itinerary_detail`, `add_itinerary`, `update_itinerary`

Edit with `PATCH /api/v1/itineraries/{id}` and a body such as `{"checklist":["Water","Rain jacket"]}`,
or MCP `update_itinerary` with `{"id":"slovensky-raj-1yo","changes":{"checklist":["Water","Rain jacket"]}}`.
Only supplied fields change. Arrays replace in full; `checklist: []` clears the list and an empty string clears optional text.
IDs cannot change. Empty updates, null values, unknown fields and invalid itineraries are rejected.
Updates require the write token, return the updated itinerary, and never create a missing itinerary.

## Private checklist editing

Use MCP `get_owner_link` with an itinerary `id`, or `POST /api/v1/itineraries/{id}/owner-link`
with the write bearer token, to get its private edit link. Opening the link enables **Upraviť zoznam**:
add, edit or remove items, then save or cancel. Checked items stay checked when renamed.
Owners can use **Zdieľať zoznam** to copy the private edit link; a selectable field is available if clipboard access fails.
The link grants checklist editing only; it does not identify a person or grant general API access.

Keys are created on demand and saved in `owner-keys.json` beside `DATA_FILE` (owner-only file permissions).
Keep this file private and include it in backups. Keys never appear in public itinerary responses.
The URL fragment is exchanged for an HttpOnly, SameSite=Strict cookie lasting 30 days, then removed from the address bar.
Keep the original link to regain access on another device. Anyone holding it can edit that checklist.
To revoke access, remove the itinerary's key from `owner-keys.json`; existing cookies stop working too.
Deleting an itinerary does not remove its key: remove the key as well before reusing an itinerary ID.

Routes: activities with a `place` become numbered stops of their day route; the optional itinerary `startLocation` (e.g. your hotel) is the origin of every day route and can be overridden in the UI (saved per itinerary in the browser). Without a start, Google Maps starts at the current location.

Distances: `GET /api/v1/itineraries/{id}/days/{day}/legs?start=…` geocodes the day's stops with OpenStreetMap Nominatim and routes each leg with the public OSRM server
(max 1 request/s, as both services require). Stops come from the stored itinerary, so the only free text a visitor sends is the start.
Stop answers, including "not found", are cached in `geo-cache.json` next to `DATA_FILE`; starts are cached in memory only.
New itineraries are looked up in the background when added, so the first visitor rarely waits. The UI shows the leg above each numbered stop, a day total, and what the start matched on the map.
Stops need names OpenStreetMap knows: the API schema and MCP instructions ask agents for official map names or street addresses; a stop it cannot find simply shows no distance.

```sh
claude mcp add --transport http itinerary https://your-host/mcp --header "Authorization: Bearer $API_TOKEN"
```

Health check: `GET /healthz`.
