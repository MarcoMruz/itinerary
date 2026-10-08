# Rodinné itineráre

Agent-ready web app for family travel itineraries. Go (stdlib only, zero dependencies) + Alpine.js + Tailwind (CDN).
Data lives in memory and is written atomically to `data/itineraries.json` on every change.

## Run

```sh
go run .                       # http://localhost:8080
go test ./...
docker build -t itinerary . && docker run -p 8080:8080 -v itinerary-data:/root/data itinerary
```

| Env          | Default                 | Purpose                                                                 |
|--------------|-------------------------|-------------------------------------------------------------------------|
| `PORT`       | `8080`                  | Listen port                                                             |
| `DATA_FILE`  | `data/itineraries.json` | JSON storage file (seeded with the default itinerary if missing)        |
| `API_TOKEN`  | *(empty = open)*        | When set, `POST`/`DELETE` and MCP `add_itinerary` need `Authorization: Bearer <token>` |
| `PUBLIC_URL` | *(from request)*        | Absolute base URL placed in `openapi.json` `servers`                     |

**Coolify:** use the Dockerfile build pack, port `8080`, add a persistent storage volume mounted at `/root/data`,
and set `API_TOKEN` (otherwise anyone who can reach the app can add or delete itineraries).

## Agent interfaces

- REST: `GET|POST /api/v1/itineraries`, `GET|DELETE /api/v1/itineraries/{id}`
- OpenAPI 3.1: `GET /api/v1/openapi.json` (GPT Actions, custom tools)
- MCP: `POST /mcp` (JSON-RPC 2.0, Streamable HTTP with JSON responses). Tools: `list_itineraries`, `get_itinerary_detail`, `add_itinerary`

```sh
claude mcp add --transport http itinerary https://your-host/mcp --header "Authorization: Bearer $API_TOKEN"
```

Health check: `GET /healthz`.
