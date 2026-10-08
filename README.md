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

- REST: `GET|POST /api/v1/itineraries`, `GET|DELETE /api/v1/itineraries/{id}`
- OpenAPI 3.1: `GET /api/v1/openapi.json` (GPT Actions, custom tools)
- MCP: `POST /mcp` (JSON-RPC 2.0, Streamable HTTP with JSON responses). Tools: `list_itineraries`, `get_itinerary_detail`, `add_itinerary`

Routes: activities with a `place` become numbered stops of their day route; the optional itinerary `startLocation` (e.g. your hotel) is the origin of every day route and can be overridden in the UI (saved per itinerary in the browser). Without a start, Google Maps starts at the current location.

```sh
claude mcp add --transport http itinerary https://your-host/mcp --header "Authorization: Bearer $API_TOKEN"
```

Health check: `GET /healthz`.
