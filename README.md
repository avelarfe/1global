# Devices API

HTTP API for storing devices and telling connected clients when a device changes state.

A device has an id, a name, a brand, a state (`available`, `in-use`, or `inactive`), and a creation time. The id and the creation time are assigned by the server. Any state may move to any other state.

## Run

Docker with Compose is enough. OrbStack and Docker Desktop both provide that CLI.

```bash
docker compose up --build
```

The API listens on [http://localhost:8080](http://localhost:8080). MySQL is not published on the host. The database user, password, and database name are all `devices`. The root password is `root`. Those values are for this local stack only.

Stop it and delete the data volume:

```bash
docker compose down -v
```

`down -v` is also how you re-run the schema. The SQL file in `migrations/` is applied by MySQL only when the data volume is empty.

Unit tests do not need Docker:

```bash
go test ./...
```

Local runs of `go test` need Go 1.27.3, the same version the image is built with. The running API does not need a Go toolchain on the host.

## API

Errors are JSON: `{"error":"message"}`.

| Method | Path | Success | Notes |
| --- | --- | --- | --- |
| `POST` | `/devices` | 201 | Body: `name`, `brand`, `state` |
| `GET` | `/devices` | 200 | Optional `brand` and `state` filters |
| `GET` | `/devices/{id}` | 200 | |
| `PUT` | `/devices/{id}` | 200 | Replaces `name`, `brand`, and `state` |
| `PATCH` | `/devices/{id}` | 200 | Updates any subset of those three |
| `DELETE` | `/devices/{id}` | 204 | |
| `GET` | `/devices/events` | stream | One event per real state change |
| `GET` | `/healthz` | 200 | Process is up |

`GET /devices` returns a JSON array, `[]` when nothing matches. Filters are exact. Brand matching is case-sensitive (`Google` and `google` are different) because the column collation is `utf8mb4_bin`. State must be one of the three lowercase values. Setting both filters means both must match. Results are ordered by creation time, then id.

```bash
id=$(curl -sS -X POST localhost:8080/devices \
  -H 'Content-Type: application/json' \
  -d '{"name":"Pixel 8","brand":"Google","state":"available"}' \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
echo "$id"
```

The created device looks like this. `created_at` is UTC, with microsecond precision.

```json
{
  "id": "6f1c0a2e-3b7d-4e19-8a44-9c2d6e5b7a10",
  "name": "Pixel 8",
  "brand": "Google",
  "state": "available",
  "created_at": "2026-10-27T15:04:05.123456Z"
}
```

`name` and `brand` are trimmed. Each must be 1 to 255 characters (Unicode characters, not bytes). There is no default state: a create without `state` is rejected.

```bash
curl -sS "localhost:8080/devices?brand=Google&state=available"
curl -sS "localhost:8080/devices/$id"
curl -sS -X PATCH "localhost:8080/devices/$id" \
  -H 'Content-Type: application/json' \
  -d '{"state":"in-use"}'
curl -sS -X PUT "localhost:8080/devices/$id" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Pixel 8","brand":"Google","state":"inactive"}'
curl -sS -X DELETE "localhost:8080/devices/$id"
```

On `PATCH`, omit a field to leave it unchanged. JSON `null` is the same as omitting it. `{}` is rejected. Unknown fields are rejected, including `id` and `created_at`.

Status codes used by the handlers:

| Code | When |
| --- | --- |
| 400 | Malformed JSON, unknown field, or a value that fails validation |
| 404 | No device has that id |
| 409 | The in-use rules below |
| 500 | Unexpected failure. The body says `internal error`; the cause is in the log |

### Rules

- Creation time is set by MySQL on insert. Clients cannot send it, and the `UPDATE` statement does not include the column.
- While a device is `in-use`, `name` and `brand` cannot change. Sending the current name and brand is fine, so the state of an in-use device can still change. The response is `409` with `name and brand cannot be updated while the device is in use`.
- An `in-use` device cannot be deleted. The response is `409` with `in-use devices cannot be deleted`. `available` and `inactive` devices can.
- A notification is sent only after the new state is committed, and only when the state actually changed.
- Creating a device does not notify. There is no previous state.
- Deleting a device does not notify. A deletion is not a state change.

The check and the write run in one transaction. The row is locked with `SELECT ... FOR UPDATE`, the rule runs against that row, then the `UPDATE` or `DELETE` runs, then the transaction commits. Two requests cannot pass the in-use check against a stale row.

### State-change stream

`GET /devices/events` is a Server-Sent Events stream. Each event is named `device_state_changed`:

```text
event: device_state_changed
data: {"device_id":"...","previous_state":"available","new_state":"in-use"}
```

```bash
curl -N localhost:8080/devices/events
```

Leave that running and change a device's state from another terminal. A name-only update, a delete, and an update that sets the state to the value it already has produce no event.

Delivery is best-effort and limited to this process. Each subscriber has a small buffer. If that buffer is full, the event is skipped for that subscriber so a slow client cannot block the request that saved the device. Events are not stored. A client that connects late, or that reconnects after a restart, does not receive earlier changes.

## Decisions

**Standard-library HTTP.** The routes are method patterns on `net/http.ServeMux` (available since Go 1.22). A router framework would hide a small API behind another project's API.

**One third-party package.** `github.com/go-sql-driver/mysql` is the driver. Queries stay in `database/sql` with `?` placeholders. An ORM would make the `UPDATE` that must not touch `created_at` less obvious, and there is only one table.

**UUID v4 in this repo.** The standard library has no UUID type. The generator is about twenty lines over `crypto/rand`. The path parameter is rejected unless it is a version-4 UUID, which is also why `GET /devices/events` is its own route: `events` is not a valid id, and it is registered as a literal path anyway.

**MySQL sets the creation time.** The `INSERT` omits `created_at` and uses the `DATETIME(6)` default. The insert and the following read share a transaction, so a failed read does not leave a row behind. The value is returned in UTC.

**Notifications go out after commit.** The service publishes only after `Commit` returns. A failed update or a failed commit publishes nothing. A write that does not change state does not publish either. The publisher is an in-memory hub, not a message broker.

**Server-Sent Events rather than webhooks.** Subscribers are clients that hold a connection. SSE is one-way, works with `curl -N`, and needs no registry of callback URLs. Webhooks would add outbound HTTP, retries, and a subscription resource the API was not asked for. WebSockets would add a second protocol for a stream that never receives client messages.

**No replay.** The hub does not remember events, and a second API container would not see the first container's events. Rebuilding that properly means a durable outbox and a shared bus. Doing it halfway (a table of events, but only inside one process) would pretend to solve a problem the service explicitly does not solve.

**Schema lives in the MySQL image entrypoint.** `migrations/001_devices.sql` is mounted into `/docker-entrypoint-initdb.d`. The official image runs those files only on an empty data directory, which is the right moment to create the table. The application does not migrate itself.

**Pinned images.** Go `1.27.3` and MySQL `26.10.0` are the current stable releases, written as tags rather than `latest`, so a later build uses the same versions. The runtime base is Alpine `3.24`. The SQL itself is ordinary InnoDB (`ENUM`, `DATETIME`, indexes) and does not depend on MySQL 26 features.

**The server has no global write timeout.** `ReadHeaderTimeout` limits slow headers. A write timeout would also cut the event stream, because the stream is one long response. Shutdown on `SIGINT` or `SIGTERM` waits a few seconds, then closes leftover streams so a container can exit.

**Row lock instead of a clever `WHERE`.** The in-use rule could be pushed into SQL (`UPDATE ... WHERE state <> 'in-use' OR (name = ? AND brand = ?)`). Keeping it in Go means the rule and the error message sit next to the other validation. The lock makes that safe. Indexes on `brand` and `state` match the two filters; the table is small enough that nothing more is justified.

## Layout

```text
cmd/api/main.go                  process, database retry, HTTP server
internal/device/device.go        device, states, validation errors
internal/device/service.go       rules, transaction, notify-after-commit
internal/device/handler.go       HTTP and the event stream
internal/device/repository.go    store interface
internal/device/mysql/repo.go    SQL
internal/notify/hub.go           in-process subscribers
internal/id/uuid.go              UUID v4
migrations/001_devices.sql
Dockerfile
docker-compose.yml
```

The handler parses HTTP. The service owns the rules. The repository reads and writes rows. Interfaces exist where the tests need a fake: the store and the publish function.

## Tests

`go test ./...` does not start MySQL. The service tests use an in-memory store and record published events. They cover validation, the in-use rename and delete rules, partial update, creation time staying put, a no-op that does not write, and a failed update or commit that does not publish and does not change the stored row. The handler tests cover status codes, the JSON shape, filters, the hidden `500` body, and a stream that receives a real state change and does not receive a delete. The SQL tests check that filter values stay in placeholders and that `INSERT` and `UPDATE` do not set `created_at`.

The Compose stack is the end-to-end check against MySQL. Inspect the rows with:

```bash
docker compose exec mysql mysql -udevices -pdevices devices \
  -e 'SELECT id, name, brand, state, created_at FROM devices'
```

## Not included

These are real gaps, left out so the service stays small enough to explain.

- Authentication and authorization.
- Pagination. `GET /devices` returns every matching row.
- A durable event log, replay after disconnect, and fan-out across more than one API process.
- An OpenAPI file. This README is the contract.
- A restricted state machine. Nothing in the domain says `inactive` cannot become `available`.
- Integration tests inside `go test`. They would need a database or a container runtime in the test process. The SQL is short and the rules are tested without it.
