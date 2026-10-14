# Rodinné itineráre

Agent-ready web app for family travel itineraries. Go (stdlib only, zero dependencies) + Alpine.js + Tailwind (CDN).
Data lives in memory and is written atomically to `data/itineraries.json` on every change.

## Run

```sh
go run .                       # http://localhost:9876
go test ./...
node --test frontend_test.mjs
docker build -t itinerary . && docker run -p 9876:9876 -v itinerary-data:/root/data itinerary
```

## Frontend structure

- `templates/index.html` composes the page from `templates/components/` (header, day route, timeline, checklist, dialogs, footer).
- `static/app.mjs` owns itinerary selection and coordinates the feature modules in `static/features/`.
- `static/domain/` holds pure route, checklist, import/export and insider-tip rules, independent of Alpine and browser APIs.
- `static/bootstrap.mjs` restores private links and starts Alpine after registering the app.

Templates and modules are embedded in the Go binary. Native JavaScript modules need no bundler or npm dependencies. Run frontend tests with Node.js 18+.

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
| `GOOGLE_CLIENT_ID`      | *(empty = off)*         | Turns on Google sign-in for the page and OAuth for MCP connectors (see below) |
| `GOOGLE_CLIENT_SECRET`  | *(empty)*               | Google OAuth client secret (required with `GOOGLE_CLIENT_ID`)           |
| `ALLOWED_EMAILS`        | *(empty)*               | Comma-separated Google accounts allowed in (required with `GOOGLE_CLIENT_ID`) |
| `OAUTH_REDIRECT_HOSTS`  | `claude.ai,claude.com,localhost,127.0.0.1` | Hosts MCP clients may register OAuth redirect URIs on (https; http only on loopback) |
| `AUTH_SECRET`           | *(generated)*           | Key signing sessions and tokens (32+ chars); by default a random key is saved in `auth-secret.key` beside `DATA_FILE` |
| `DISTANCES`             | `true`                  | Driving distance between route stops (`false` to disable; place names are then never sent to OpenStreetMap) |
| `WEATHER`               | `true`                  | Daily weather forecast from Open-Meteo for the selected day (`false` to disable; also off when `DISTANCES=false`, since places are geocoded with OpenStreetMap) |
| `START_LOOKUPS_PER_MINUTE` | `20`                 | Uncached lookups (geocode, start→first stop, weather forecast) one client may cause per minute, burst half of it; all clients together are capped at 30/min so stops always get through (`0` disables both). Keyed by client IP, so set `CLIENT_IP_HEADER` behind a proxy |

**Coolify:** use the Dockerfile build pack, port `9876`, add a persistent storage volume mounted at `/root/data`,
and set `API_TOKEN` (otherwise anyone who can reach the app can read, add or delete itineraries).

## Security

- **Access:** once a token is set, `/api/v1/itineraries*` and `/mcp` need `Authorization: Bearer <token>`.
  The web page sets an `HttpOnly`, `SameSite=Strict` read-only cookie so the UI keeps working; it cannot write.
  The page itself stays public unless Google sign-in is on (see below).
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

## Google sign-in and MCP connectors

With `GOOGLE_CLIENT_ID` set, the page shows only a Google sign-in until someone from `ALLOWED_EMAILS` logs in,
so crawlers and strangers see no itinerary content. The same sign-in lets MCP clients connect over OAuth,
e.g. a claude.ai custom connector that then also works in the Claude mobile and desktop apps.

1. Google Cloud Console → APIs & Services → Credentials → Create OAuth client ID → *Web application*.
   Authorized redirect URI: `https://<your-host>/auth/google/callback`. (OAuth consent screen: scopes `openid` and `email`.)
2. Set `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`, `ALLOWED_EMAILS` and `PUBLIC_URL=https://<your-host>`; redeploy.
3. claude.ai → Settings → Connectors → *Add custom connector* → URL `https://<your-host>/mcp`, no client id/secret.
   Sign in with Google, then click **Povoliť** on the consent page.

How it works: the app is its own OAuth 2.1 authorization server (metadata at `/.well-known/oauth-authorization-server`
and `/.well-known/oauth-protected-resource/mcp`, dynamic client registration, PKCE S256 required) and uses Google only
to learn who signs in. Every allowed account gets full read/write access through MCP; on the page a signed-in account can read and import.
Access tokens last 1 hour, refresh tokens 60 days, web sessions 30 days. Client ids, codes and tokens are signed with
`AUTH_SECRET` rather than stored. Removing an email from `ALLOWED_EMAILS` revokes that person at once;
changing `AUTH_SECRET` (or deleting `auth-secret.key`) signs everyone out and disconnects every connector.
`API_TOKEN` and `READ_API_TOKEN` keep working for scripts and Claude Code.

If Cloudflare "Block AI bots" or Bot Fight Mode is on, make sure it does not block `/mcp`, `/oauth/*` and `/.well-known/*`,
or the claude.ai connector cannot reach the app.

## Agent interfaces

- REST: `GET|POST /api/v1/itineraries`, `GET|PATCH|DELETE /api/v1/itineraries/{id}`
- OpenAPI 3.1: `GET /api/v1/openapi.json` (GPT Actions, custom tools)
- MCP: `POST /mcp` (JSON-RPC 2.0, Streamable HTTP with JSON responses). Tools: `list_itineraries`, `get_itinerary_detail`, `add_itinerary`, `update_itinerary`

Edit with `PATCH /api/v1/itineraries/{id}` and a body such as `{"checklist":["Water","Rain jacket"]}`,
or MCP `update_itinerary` with `{"id":"slovensky-raj-1yo","changes":{"checklist":["Water","Rain jacket"]}}`.
Only supplied fields change. Arrays replace in full; `checklist: []` clears the list and an empty string clears optional text.
IDs cannot change. Empty updates, null values, unknown fields and invalid itineraries are rejected.
Updates require the write token, return the updated itinerary, and never create a missing itinerary.

## Import / export for AI assistants

The **Import / Export** button on the page lets ChatGPT, Gemini or Claude write itineraries without any integration:

1. **Exportovať** downloads the current itinerary (`GET /api/v1/itineraries/{id}/export`, action `update`);
   **Stiahnuť šablónu** downloads an example skeleton (`GET /api/v1/import/template`, action `create`).
   **Kopírovať** puts the same file on the clipboard for pasting into a chat.
2. Give the file to the assistant and say what to create or change. The file carries the rules and the JSON Schema,
   so the assistant answers with the complete document in the same format.
3. Paste the answer (or pick the returned file) and click **Skontrolovať**: a preview shows whether it adds a new itinerary
   or replaces an existing one and which fields change. **Uložiť** saves it.

`POST /api/v1/import` (add `?dryRun=true` for the preview) accepts the export document or a bare itinerary,
also wrapped in a ```` ```json ```` block or surrounded by text. `update` replaces the whole itinerary with that id
(omitted fields are cleared, unlike `PATCH`); `create` adds a new one; without an action an existing id means update.
Importing needs write access: `API_TOKEN` as a bearer token (the dialog asks for it), or a Google sign-in
on the page itself (same-origin requests only).

## Local insider tips

Tick **Pridať rady od miestneho** in the Import / Export dialog before exporting or copying. The file then asks the assistant
to act as a local, research the destination in depth and fill in (`?insider=true` on the export and template URLs):

- `insight` on each activity: `summary`, `bestTime`, `tips`, `avoid`, `sources`. The summary shows on the timeline card;
  the rest opens on its own page (`#<id>/d/<day>/<activity>`).
- `localGuide` on the itinerary: `summary`, `gettingAround`, `tips`, `avoid`, `sources`, opened from **Miestny sprievodca**
  or **Ako sa tu pohybovať** (`#<id>/guide`).

Lists hold at most 15 items, sources at most 10 http(s) links. Blank insights are dropped; `PATCH` with `{"localGuide":{}}` clears the guide.

## Read-only share links

**Zdieľať** (header on larger screens, side menu on phones) creates a link like `https://<host>/s/<id>/<key>`
that opens that one itinerary without signing in, even with Google sign-in on. The visitor cannot change it,
does not see the list or other itineraries, and gets no Import / Export or sharing actions.
Checklist ticks stay in their own browser, as for everyone.

- Create or get: `POST /api/v1/itineraries/{id}/share-link` (write token, or signed in on the page) or MCP `get_share_link`.
- Revoke: `DELETE /api/v1/itineraries/{id}/share-link` or **Zrušiť odkaz** in the dialog. The old link and the cookies
  it issued stop working at once; sharing again creates a new link.
- Keys are stored in `share-keys.json` beside `DATA_FILE` (owner-only permissions); keep it with your backups.

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

Weather: `GET /api/v1/itineraries/{id}/days/{day}/weather?date=YYYY-MM-DD` returns the daily forecast from [Open-Meteo](https://open-meteo.com/) (open source, no API key, CC BY 4.0)
at the day's first stop, else the itinerary `startLocation`. The date comes from `?date=`, else the day's own `date`, else the itinerary `startDate` plus the day index.
The UI shows a weather card for the selected day; visitors can pick the trip start date there (saved per itinerary in the browser) when the itinerary has none.
Past days answer `status: "past"` and the UI hides the card for them; forecasts reach about 15 days ahead, a later date answers `status: "too_far"` with `availableFrom`.
With weather off the endpoint answers `status: "disabled"`. Forecasts are cached on the server for an hour, and its answers (other than `disabled` and errors) carry `Cache-Control: private, max-age=<seconds left>`, so the browser reuses them until the server entry expires. The UI adds `&place=` (ignored by the server) so changing the day's place misses the browser cache. Concurrent requests for the same coordinates and date share one forecast lookup. Uncached geocodes and forecasts each count towards `START_LOOKUPS_PER_MINUTE`; forecasts refill at 6/min across all clients with a burst of 60.

```sh
claude mcp add --transport http itinerary https://your-host/mcp --header "Authorization: Bearer $API_TOKEN"
```

Health check: `GET /healthz`.
