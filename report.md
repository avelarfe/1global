# Devices API — interview notes

This file is the speaking script for the code in this repository. Read the first sections out loud. Use the file-by-file section when you want to point at a specific line and say what it does and why it is there.

The API stores devices and tells clients that are connected right now when a device changes state. A device has an id, a name, a brand, a state (`available`, `in-use`, or `inactive`), and a creation time. The server assigns the id and the creation time. Any state may move to any other state. While a device is `in-use`, its name and brand stay as they are, and the device cannot be deleted.

---

## How to open

Say this, then stop and let them ask.

> This is a small Go HTTP service. Docker Compose starts the API and MySQL with one command. The API is the standard library: `net/http` for routes, `database/sql` plus the MySQL driver for the one table, and an in-process hub for Server-Sent Events.
>
> The handler parses HTTP. The service owns the rules. The repository owns the SQL. I publish a state change only after the transaction commits, and only when the state value actually changed. Creating a device and deleting a device do not notify. There is no previous state on create, and a delete is not a state change.
>
> The in-use check and the write share one transaction. The row is locked with `SELECT ... FOR UPDATE`, the rule runs against that row, then the `UPDATE` or `DELETE` runs. Two requests cannot both pass the check against a stale row.
>
> Unit tests cover the rules with an in-memory store, so `go test ./...` does not need Docker. Postman, run by Newman, is the check against the real server and the real database. The event stream is in that collection, and the runner skips it, because that call stays open.

If they ask "what would you add in production?", the honest list is already at the bottom of the README: authentication, pagination, a durable event log, replay, and fan-out across more than one API process. Say that those were left out so the service stays small enough to explain, and say what you would add first (auth, then a transactional outbox if more than one instance has to see the events).

---

## What the assignment required

- A Go HTTP API for devices.
- Fields: id, name, brand, state (`available`, `in-use`, `inactive`), creation time.
- Creation time is immutable.
- Name and brand cannot change while the device is in use.
- An in-use device cannot be deleted.
- Operations: create, full update, partial update, fetch one, fetch all, filter by brand, filter by state, delete.
- Notify subscribers of a state change with the device id, the previous state, and the new state, and only after the new state is persisted.
- Recovering a notification the client missed is not required.
- It must compile and run, have tests and documentation, use a real database, be containerized, and be a git repository with a README.

That last bullet is why Docker and Postman exist. The unit tests prove the rules. They do not prove that a reviewer can start the stack and exercise the HTTP contract. Compose and the Postman collection do that.

---

## The map

```text
cmd/api/main.go                  process, database retry, HTTP server, request log
internal/device/device.go        device, states, validation errors, JSON shape
internal/device/service.go       rules, transaction, notify-after-commit
internal/device/handler.go       HTTP and the event stream
internal/device/repository.go    Store and Tx interfaces
internal/device/mysql/repo.go    SQL
internal/device/mem_test.go      in-memory store used only by tests
internal/notify/hub.go           in-process subscribers
internal/id/uuid.go              UUID v4
migrations/001_devices.sql       the only schema change
Dockerfile                       static binary on Alpine
docker-compose.yml               API + MySQL
postman/                         HTTP contract against the running stack
```

One request crosses three layers:

1. `handler.go` reads JSON, calls the service, writes a status code.
2. `service.go` trims and validates, locks the row when the call can change or delete it, commits, then publishes.
3. `mysql/repo.go` runs parameterized SQL. It does not know the in-use rule.

Interfaces exist where a test needs a fake: `Store`, `Tx`, and the `publish` function. The HTTP stack and the UUID helper are not hidden behind interfaces. The handler tests start a real `httptest` server against the real handler, with the in-memory store behind the service.

---

## Decisions, in the order they will ask

### Standard-library HTTP

Routes are method patterns on `net/http.ServeMux`, for example `"PATCH /devices/{id}"`. That form has been in the standard library since Go 1.22. `go.mod` says `go 1.27.3`, so it is available. A router framework would put a small API behind another project's API, and this service has eight routes.

`PUT /devices` is not registered. The mux answers `405` with the plain-text body `Method Not Allowed`. That is the standard library's response, and the Postman request "Reject PUT on the collection" asserts it. The JSON error shape is for the handlers I wrote. I did not wrap the mux to turn 405 and 404-for-unknown-paths into JSON.

### One third-party package

`github.com/go-sql-driver/mysql` is the only direct dependency. `filippo.io/edwards25519` is an indirect dependency of that driver. Queries stay in `database/sql` with `?` placeholders.

An ORM would hide the statement that must not touch `created_at`. There is one table. The `UPDATE` is four columns and a `WHERE`. Keeping it as a string constant means a test can assert that `insertSQL` and `updateSQL` do not contain `created_at`.

### UUID v4 in this repository

The standard library has no UUID type. The generator in `internal/id/uuid.go` is `crypto/rand` plus the version and variant bits from RFC 4122. The path parameter is rejected unless it matches version 4, RFC variant.

That check is also why `GET /devices/events` has to be its own route. If it were not registered, the mux would treat `events` as `{id}` and the service would return `400 invalid device id`. The literal route is registered as well, so the stream exists. `TestValidateRejects` includes the string `events` so that reason stays visible.

The id is `CHAR(36)`, the hyphenated text form, because the API and the logs speak that form. A `BINARY(16)` column would save space and would make every query decode bytes. The table is one resource for this assignment. Random v4 values scatter InnoDB's clustered index. At this size that does not matter. If the table grew large, the id would become time-ordered (UUID v7) or the primary key would become a surrogate, with the UUID as a unique column.

### MySQL sets the creation time

`created_at` is `DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)`. The `INSERT` lists `id, name, brand, state` and omits `created_at`. The `UPDATE` sets `name`, `brand`, and `state` only. A client cannot send either field: `DisallowUnknownFields` rejects `id` and `created_at` in the body with `400`.

The insert and the following `SELECT` share a transaction. The `SELECT` exists so the response contains the database clock, not a time the Go process invented. If that read fails, the rollback removes the row. The value is formatted in UTC with six fractional digits, which matches `DATETIME(6)`.

UTC is pinned in three places, because any one of them can drift:

- The MySQL container runs with `TZ=UTC` and `--default-time-zone=+00:00`, so `CURRENT_TIMESTAMP(6)` is UTC. `DATETIME` does not convert time zones on read the way `TIMESTAMP` does, so the session time zone has to be UTC at insert time.
- The DSN has `parseTime=true&loc=UTC`, so the driver scans the column into a `time.Time` in UTC.
- `scanDevice` calls `.UTC()` again, and `MarshalJSON` formats with `.UTC()`.

### The in-use rule lives in Go, next to a row lock

The rule could be a `WHERE` clause:

```sql
UPDATE devices
SET name = ?, brand = ?, state = ?
WHERE id = ?
  AND (state <> 'in-use' OR (name = ? AND brand = ?))
```

The rule and the message `"name and brand cannot be updated while the device is in use"` would then be split between SQL and Go. A `0 rows affected` result would also be ambiguous: missing row, or rule rejected. The code locks the row, applies the rule in `validateChange`, and returns `ConflictError` or `ErrNotFound` as two different types. The handler maps those to `409` and `404`.

`SELECT ... FOR UPDATE` inside the transaction is what makes the Go check safe. InnoDB holds the row lock until commit or rollback. A second request blocks in `GetForUpdate` until the first transaction finishes, then reads the new row.

The check runs before the field checks. A rename of an in-use device is `409` even when the new name would also fail validation. That order is the comment on `validateChange`. Sending the current name and the current brand is allowed, so the state of an in-use device can still change.

### Notify only after commit, and only on a real state change

`mutate` calls `tx.Commit()` and then, only if `next.State != current.State`, calls `publish`. A failed `UPDATE` returns before commit. A failed `Commit` returns before `publish`. A name-only change commits and does not publish. A patch that sets the state to the value it already has returns the current row and does not call `UPDATE` at all.

Create does not publish. There is no previous state. Delete does not publish. A deletion is not a state change. Both are tested.

The publish call sits after the lock is released. Two commits that land close together can in theory publish out of order if the first goroutine is scheduled out between `Commit` and `publish`. The window is a few instructions and the hub does not block, so it is not a practical bug at one process. It is the reason a durable outbox with a single publisher is the real fix once more than one process, or a strict event order, matters. I would say that if they push on it.

### Server-Sent Events, in memory, no replay

The assignment says to notify subscribers after persistence, and it says missed-notification recovery is not required. Subscribers are clients that hold a connection.

SSE fits that. It is one HTTP response that stays open, it works with `curl -N`, and the client never sends a message. Webhooks would add outbound HTTP, retries, and a subscription resource the API was not asked for. WebSockets would add a second protocol for a stream that never receives client messages.

The hub is a mutex and a map of channels. Each channel has a buffer of 8. `Publish` uses a `select` with a `default`. If the buffer is full, that event is skipped for that subscriber and the request that saved the device continues. A slow client cannot stall the save.

Events are not stored. A client that connects late, reconnects, or talks to a second API container does not receive earlier changes. A table of events that only the local process can see would look like replay without being replay. The README says that on purpose. The production shape, if they ask, is a transactional outbox in MySQL and a shared bus, with the SSE handler reading from that bus.

### Schema in the MySQL image entrypoint

`migrations/001_devices.sql` is mounted at `/docker-entrypoint-initdb.d/001_devices.sql`. The official MySQL image runs those files only when the data directory is empty. The application does not migrate itself. One table does not need a migration library, and the application user does not need permission to change the schema.

The consequence is operational and you should say it: editing the SQL file does nothing to an existing volume. `docker compose down -v` deletes the volume so the next start runs the script again.

### Pinned images

`golang:1.27.3`, `mysql:26.10.0`, and `alpine:3.24` are tags, not `latest`. A later build uses the same versions. The SQL is ordinary InnoDB: `ENUM`, `DATETIME(6)`, two secondary indexes. It does not depend on a MySQL 26 feature.

### No global write timeout

`http.Server.ReadHeaderTimeout` is 5 seconds. That limits a client that connects and dribbles the headers. `WriteTimeout` is left unset. A write timeout applies to the whole response, and the event stream is one long response, so a write timeout would cut every subscriber. Shutdown on `SIGINT` or `SIGTERM` waits 5 seconds through `Shutdown`, then calls `Close` so a container can still exit while streams are open.

`ReadHeaderTimeout` is also why a slow-header connection cannot pin a goroutine forever. The body is limited separately, at 1 MiB, by `http.MaxBytesReader`.

### Row shape and collation

`brand` and `state` each have an index because those are the two filters. A query that sets both is `AND`. The result is ordered by `created_at`, then `id`, so the order is stable when two rows share a timestamp.

The table collation is `utf8mb4_bin`. Brand matching is byte-for-byte, so `Google` and `google` are different. That is a property of the column, which is why the Postman request "Brand filter is case-sensitive" runs against MySQL. The Go unit test compares strings, which are also case-sensitive, but it never opens a database, so it cannot prove the collation.

`state` is an `ENUM` of the three values. The service rejects a bad state with `400` before the driver sees it. The `ENUM` is a backstop. Without the Go check, a bad value would surface as a driver error and the client would get `500 internal error`.

### `healthz` is liveness of the process

`GET /healthz` returns `{"status":"ok"}` and does not ping MySQL. It answers "the process is serving." Compose already waits until `mysqladmin ping` succeeds, and the process retries the database for 30 seconds before it starts listening. If MySQL dies later, `/healthz` stays green and device calls return `500`. Say that plainly if they ask. A readiness check would ping the database and would be a different route.

### What was left out, and the sentence for each

| Left out | The sentence |
| --- | --- |
| Authentication | The assignment is the device resource and the notification rule. Auth would be middleware in front of `Routes`, and it would not change the service. |
| Pagination | `GET /devices` returns every matching row. A cursor on `(created_at, id)` matches the order the query already uses. |
| Durable events, replay, more than one API process | The hub is process memory. The honest next step is an outbox, not a slice of old events inside this process. |
| OpenAPI | The README is the contract, and the Postman collection asserts the status codes and the error strings. |
| A state machine | Nothing in the domain says `inactive` cannot become `available`. Any state may follow any other state. |
| Integration tests inside `go test` | They would need a database in the test process. The rules are tested with a fake store. The SQL strings are tested directly. The running stack is tested with Newman. |
| An ORM, webhooks, Redis | Covered above. Each one adds a moving part the assignment does not need. |

---

## One request, end to end

`PATCH /devices/{id}` with `{"state":"in-use"}` while the stored state is `available`.

1. `logRequests` wraps the `ResponseWriter` and records the start time. The wrapper's `Flush` method forwards to the real writer. The stream needs that. This request does not.
2. The mux matches `"PATCH /devices/{id}"`.
3. `readJSON` limits the body to 1 MiB, rejects unknown fields, decodes into `PatchInput`, and requires that the body contain a single JSON value. `State` is a `*string`. It is non-nil. `Name` and `Brand` stay nil.
4. `Service.Patch` rejects a body where all three pointers are nil. This body has a state, so it calls `mutate`.
5. `validateDeviceID` checks the version-4 pattern. A bad id is `400` and never opens a transaction.
6. `Repository.Begin` starts a transaction. `defer tx.Rollback()` runs on every return. After a successful commit, `Rollback` sees `sql.ErrTxDone` and returns nil.
7. `GetForUpdate` runs `SELECT ... WHERE id = ? FOR UPDATE`. No row is `ErrNotFound`, which becomes `404`.
8. The patch function copies the locked row, trims the new state, and calls `validateChange`. The current state is `available`, so the in-use rename rule does not apply. The new state is valid.
9. `mutate` puts the locked id and `created_at` back onto the new value, so the callback cannot change them.
10. The state differs, so `UPDATE` runs. `created_at` is not in that statement.
11. `Commit` returns. The row is durable.
12. The states differ, so `publish` sends `StateChanged` to the hub. `Publish` does not wait for subscribers.
13. The handler writes `200` and the device JSON. `created_at` is the original value, formatted with six digits and a `Z`.
14. The middleware writes one log line with method, path, status, and duration. The path does not include the query string.

The same path, with the device already `in-use` and a different name, stops at step 8 with `ConflictError`. There is no `UPDATE`, no commit, and no event. The deferred rollback releases the lock. Postman covers that as "Reject a brand change while in use" and "Reject a full rename while in use".

A patch that sets the name to the name already stored returns at the equality check in `mutate`. No `UPDATE`, no commit, no event. The client still gets `200` and the current device.

---

## `cmd/api/main.go`

This file is the process. It does not know the device rules.

### Imports and `main` up to the database

```go
slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
```

Logs go to stderr as text. The Docker container's log stream is stderr. The level filter is the default, so `Info` and `Error` both appear.

`HTTP_ADDR` defaults to `:8080`. `DB_DSN` has no default. An empty DSN exits with status 1 before any listen. The binary will not guess a database.

`sql.Open` does not connect. It prepares the pool. The driver is registered by the blank import of `github.com/go-sql-driver/mysql`.

Pool settings: at most 10 open connections, 5 of them idle, each connection recycled after 30 minutes. The service is one small API. The cap stops a slow MySQL from letting goroutines open connections without bound. `ConnMaxLifetime` recycles connections the server may have closed.

### `waitForDB`

Compose waits for `mysqladmin ping` before it starts the API. The process still retries, because a healthy ping and a successful login as `devices` are not the same moment. `waitForDB` pings once a second until the ping works or the 30 second context fires. On timeout it returns the last ping error, and `main` exits 1. The container restarts only if the orchestrator restarts it. Compose, as written, does not restart it. The log line is `database not ready`.

### Wiring

```go
hub := notify.NewHub()
service := device.NewService(devicemysql.NewRepository(db), hub.Publish, id.New)
server := &http.Server{
    Addr:              addr,
    Handler:           logRequests(device.NewHandler(service, hub).Routes()),
    ReadHeaderTimeout: 5 * time.Second,
}
```

`hub.Publish` is a method value. It matches `func(notify.StateChanged)`. The service depends on that function, not on the hub type, so tests pass a recorder. The handler holds the hub because the stream has to `Subscribe`. `id.New` is the production id generator. Tests pass a function that returns a fixed UUID.

`ReadHeaderTimeout` is set. `WriteTimeout`, `ReadTimeout`, and `IdleTimeout` are not. The reason is the stream, covered above.

### The server goroutine and shutdown

`ListenAndServe` runs on its own goroutine and sends its return value to `errCh`. The main goroutine waits on that channel and on `SIGINT` / `SIGTERM`.

If the listener fails, and the error is not `http.ErrServerClosed`, the process logs and exits 1.

On a signal, `Shutdown` waits up to 5 seconds for in-flight requests. An open event stream is an in-flight request, so `Shutdown` usually hits the deadline. The code then logs `closing open connections` and calls `Close`, which drops the connections. The comment in `main` says that. The container can exit.

`signal.Notify` is buffered with size 1. One signal is enough. A second signal during shutdown is not given a fast path. Five seconds is the bound.

The database is not closed explicitly. Process exit closes it.

### `getenv`

Returns the environment value when it is non-empty, otherwise the fallback. Used only for `HTTP_ADDR`.

### `statusWriter` and `logRequests`

`statusWriter` embeds `http.ResponseWriter` and remembers the status code. The field starts at `200` because a handler that never calls `WriteHeader` still produced a 200. `http.ResponseWriter.Write` calls `WriteHeader(200)` on the way through, which hits the override, so the recorded status matches what the client got.

`Flush` is implemented on the wrapper. `GET /devices/events` type-asserts the writer to `http.Flusher`. If the wrapper did not implement `Flush`, that assert would fail and the stream would return an error. `TestStatusWriterForwardsFlush` writes a 204, flushes, and checks both the stored status and `httptest.ResponseRecorder.Flushed`.

`logRequests` logs after `next.ServeHTTP` returns. For the event stream, that is when the client disconnects, and `duration` is how long the stream was open. The log fields are method, path, status, and duration. Query strings are not logged.

The wrapper does not implement `Unwrap`. Nothing in this server uses `http.ResponseController`. The stream finds `Flush` on the wrapper itself.

---

## `internal/device/device.go`

### Error strings

Every client-facing message is a package-level constant. The service returns the constant. The handler writes it. The tests compare against the same constant. The Postman collection asserts the same text. If a message changes, both suites move together only if someone updates both. The Go tests will catch a Go-side drift. Newman catches a drift between the binary and the collection.

| Constant | HTTP | When |
| --- | --- | --- |
| `name is required` | 400 | Missing or blank name |
| `name must be at most 255 characters` | 400 | More than 255 runes |
| `brand is required` | 400 | Missing or blank brand |
| `brand must be at most 255 characters` | 400 | More than 255 runes |
| `state must be available, in-use, or inactive` | 400 | Missing, blank, or unknown state, including a bad `?state=` filter |
| `at least one of name, brand, or state is required` | 400 | `PATCH` body `{}` or all nulls |
| `name and brand cannot be updated while the device is in use` | 409 | In-use row, name or brand would change |
| `in-use devices cannot be deleted` | 409 | `DELETE` of an in-use row |
| `invalid device id` | 400 | Path id is not a UUID v4 |
| `brand filter cannot be empty` | 400 | `?brand=` empty or only spaces |
| `device not found` | 404 | No row |
| `internal error` | 500 | Anything else. The cause is logged, not returned |

`ErrNotFound` is a sentinel. `errors.Is` matches it. `ValidationError` and `ConflictError` are structs with a `Message`, matched with `errors.As`. They are different types so the handler can pick 400 versus 409 without parsing the sentence.

### `State`

A named `string` with three constants. `Valid` is a switch. There is no transition table. `available` can become `inactive` directly. That is the domain in the assignment.

### `Device` and `MarshalJSON`

The Go struct keeps `CreatedAt` as `time.Time`. The JSON shape is a private `deviceJSON` struct so the wire format stays under the type's control. `created_at` is `CreatedAt.UTC().Format("2006-01-02T15:04:05.000000Z")`. The zeros in the layout mean "always six digits", so a whole second is still `.000000Z`. Clients can compare the string to the value they stored at create time. Postman does that with the `createdAt` collection variable.

There is no `UnmarshalJSON`. Clients never send a `Device`. They send `CreateInput`, `ReplaceInput`, or `PatchInput`.

### Validators

`validateName` and `validateBrand` reject the empty string, then count runes with `utf8.RuneCountInString`. The limit is 255 characters, not 255 bytes. `VARCHAR(255)` under `utf8mb4` is also 255 characters. `TestCreateAllows255Runes` uses 255 copies of `á`, which is two bytes and one rune. A byte-length check would have rejected a legal name.

`validateState` returns the shared state message for every failure, including the empty string. A create body with no `state` field decodes as `""`, and the client sees `state must be available, in-use, or inactive`. There is no separate "state is required" message, and there is no default state.

### `validateChange`

```go
if current.State == StateInUse && (next.Name != current.Name || next.Brand != current.Brand) {
    return ConflictError{Message: errInUseRename}
}
```

Then name, brand, and state are validated.

The current state is the one that matters. An `available` device may be renamed and moved to `in-use` in one request. An `in-use` device may move to `inactive` in one request when the name and brand in the new value equal the stored ones. A change of name that is only surrounding whitespace disappears before this function, because the service trims first. Sending the same name with spaces around it is not a rename.

---

## `internal/device/repository.go`

`Store` is what the service needs from a database:

- `Insert` returns the stored row, including the database `created_at`.
- `Get` and `List` are the reads that do not take a lock.
- `Begin` starts the transaction used by update and delete.

`Tx` is the locked unit of work:

- `GetForUpdate` reads the row and locks it.
- `Update` and `Delete` run on that transaction.
- `Commit` and `Rollback`.

The service begins a transaction only for replace, patch, and delete. Create uses `Insert`, which has its own short transaction inside the MySQL repository. List and get do not need a transaction.

`List` takes `*string` for brand and state. A nil pointer means "this filter was not sent". An empty string would be a real filter value, and the service rejects that before the repository sees it.

---

## `internal/device/service.go`

### Inputs

`CreateInput` and `ReplaceInput` use plain strings. A missing JSON field becomes `""`, which fails validation. The client has to send all three on create and on full replace.

`PatchInput` uses `*string`. The encoding rules the handler relies on:

- Omitted field: nil, leave the column alone.
- JSON `null`: nil, same as omitted. Postman request "Treat null as an omitted field".
- JSON `""`: pointer to `""`, which is a real update, and then fails "required" unless the in-use rule fires first.

`{}` is all nils. `Patch` returns `errPatchEmpty` before it opens a transaction. An empty patch does not lock a row.

### `NewService`

A nil repository, publish function, or id function panics. That is a startup bug. `main` never passes nil. A panic here is louder than a nil dereference on the first request.

### `Create`

Trims name, brand, and state. Validates name, then brand, then state. Generates the id only after validation, so a bad body does not spend a random id. Calls `repo.Insert`. Does not call `publish`.

The id generator can fail if `crypto/rand` fails. That error is not a `ValidationError`, so the handler returns `500` and logs it.

### `Get`

Validates the id, then `repo.Get`. Invalid id is 400. Unknown id is 404. The string `events` fails validation. It never reaches the database.

### `List`

If `brand` is non-nil and `TrimSpace` is empty, the error is `brand filter cannot be empty`. A brand filter that is not only whitespace is passed through as the client sent it. `"Google"` matches. `"google"` does not. `" Google"` does not match `"Google"`, because the filter is not trimmed. Name and brand on write are trimmed. The filter is exact. Say that if they compare the two.

If `state` is non-nil, it is trimmed and then validated. `?state= available` is accepted as `available`. A bad state is 400 and does not hit the database.

The repository result is turned into an empty slice when it is nil, so the JSON is `[]` and not `null`. The MySQL repository already allocates an empty slice. The service still guards, because `Store` is an interface and another implementation might return nil.

### `Replace` and `Patch`

Both call `mutate`. Replace overwrites all three fields from the body. Patch overwrites a field only when its pointer is non-nil. Both trim. Both then call `validateChange`.

### `Delete`

Validates the id, begins a transaction, locks the row. If the locked state is `in-use`, returns `ConflictError` and the deferred rollback runs. Otherwise deletes and commits. No `publish` on either path.

The in-use check uses the locked row, not a row read earlier. A request that flips the device to `in-use` and a delete that started at the same time are ordered by the lock.

### `mutate`

This is the function to walk slowly if they ask about correctness.

```go
defer tx.Rollback()
current, err := tx.GetForUpdate(ctx, deviceID)
next, err := fn(current)
next.ID = current.ID
next.CreatedAt = current.CreatedAt
if next.Name == current.Name && next.Brand == current.Brand && next.State == current.State {
    return current, nil
}
if err := tx.Update(ctx, next); err != nil {
    return Device{}, err
}
if err := tx.Commit(); err != nil {
    return Device{}, err
}
if next.State != current.State {
    s.publish(...)
}
return next, nil
```

Line by line:

- The id is validated before `Begin`, so a bad id costs nothing.
- `Rollback` is deferred immediately after `Begin`. Every error path rolls back. `Rollback` after `Commit` is a no-op in both implementations (`sql.ErrTxDone` in MySQL, a `finished` flag in the fake).
- `GetForUpdate` is the lock and the snapshot the rule sees.
- `fn` is the replace or patch logic. It returns a validation or conflict error without writing.
- The id and creation time are copied back from `current` after `fn` returns. The callback cannot change them even if it tried.
- Equal name, brand, and state means the write is a no-op. The function returns the locked snapshot. It does not call `Update` or `Commit`. The deferred rollback releases the lock. `TestPartialUpdateAndNoop` asserts `updates` stays put and nothing is published.
- `Update` then `Commit`. Publish is after `Commit` returns. The comment in the function says why: publishing earlier would tell clients about a state that might still roll back.
- Publish carries the id from the locked row, the previous state, and the new state. Those are the three fields the assignment asked for.
- The value returned to the handler is `next`, which has the original id and creation time and the new name, brand, and state.

---

## `internal/device/handler.go`

### `Handler` and `Routes`

`NewHandler` panics on a nil service or hub, same startup-bug policy as the service.

Route table, in the order it is registered:

| Pattern | Handler | Success |
| --- | --- | --- |
| `GET /healthz` | `health` | 200 `{"status":"ok"}` |
| `POST /devices` | `create` | 201 and the device |
| `GET /devices` | `list` | 200 and a JSON array |
| `GET /devices/events` | `events` | 200, `text/event-stream`, stays open |
| `GET /devices/{id}` | `get` | 200 and the device |
| `PUT /devices/{id}` | `replace` | 200 and the device |
| `PATCH /devices/{id}` | `patch` | 200 and the device |
| `DELETE /devices/{id}` | `remove` | 204, empty body |

`GET /devices/events` is registered as a literal. The comment says `events` must not be treated as an id. In Go's mux, a more specific pattern wins over `{id}` anyway. The literal is still the route that implements the stream. Without it, `events` would fall through to `{id}`.

`{id}` is read with `r.PathValue("id")`.

### The CRUD handlers

Each one decodes (when there is a body), calls one service method, and writes the status. They do not validate the device. They do not branch on the in-use rule. `writeError` does the status mapping.

`list` builds `*string` filters only when the query key is present. `?brand=` is present and empty, so the service rejects it. A missing key is a nil pointer, and the filter is not applied. `?brand=Google&state=available` means both.

`remove` writes `204` with `WriteHeader` and no body.

### `events`

This is the stream.

- The writer must implement `http.Flusher`. The middleware wrapper does. A writer that does not is an error response. `httptest.ResponseRecorder` implements `Flush`, which is why the handler test can drive the stream. The test still uses its own recorder, because it has to wait until a flush happens. The stock recorder does not signal that.
- Headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`. The last one tells nginx not to buffer the stream. Nothing in Compose puts a proxy in front, and the header is harmless when no proxy is there.
- Status `200` is written before any event.
- `Subscribe` registers a channel. `defer cancel()` removes it when the handler returns. The hub does not close the channel. A closed channel would make `case event := <-events` receive a zero value forever and the handler would write empty events in a loop. The context ending is what stops the loop.
- The first write is `: connected\n\n`. A line that starts with `:` is an SSE comment. It is not an event. It gives the client proof that the stream is open, and it gives proxies bytes to flush. Postman asserts the substring `: connected`.
- The loop selects on `r.Context().Done()` and on the channel. Disconnect, or server shutdown closing the connection, ends the handler. `cancel` runs. Later publishes skip this client.
- Each event is `event: device_state_changed\ndata: {json}\n\n`. The JSON is `device_id`, `previous_state`, `new_state`, from the struct tags on `notify.StateChanged`.
- A marshal error is logged and skipped for this client. `StateChanged` contains three strings, so marshal does not fail in practice. The branch is there so a future field cannot kill the stream.
- `Flush` after the comment and after every event. Without it the client sits on a buffered response and sees nothing until the buffer fills.

The handler subscribes before it tells the client it is connected, and the publish happens after the database commit. A state change that commits before `Subscribe` is not delivered. That is the "no replay" rule, visible in the order of these lines.

### `readJSON`

`http.MaxBytesReader` at `1 << 20` (1 MiB). Over the limit, decode fails and `jsonError` returns `request body is too large`, which is a 400.

`DisallowUnknownFields` is why a client-supplied `id` or `created_at` is `400` with `unknown field "..."`. The same call rejects any other extra field.

The decoder reads one value. A second `Decode` into an empty struct must return `io.EOF`. Anything else means a second JSON value followed the first, and the error is `request body must contain a single JSON value`. Trailing whitespace is fine. The decoder skips it.

`jsonError` maps:

- `*http.MaxBytesError` to `request body is too large`.
- `io.EOF` to `request body is required`. An empty body hits this.
- A message that starts with `json: unknown field ` to that message with the `json: ` prefix removed. The client sees `unknown field "created_at"`.
- Every other decode error to `invalid JSON`. The raw Go error stays off the wire. A syntax error can quote a piece of the body. The client gets a fixed sentence.

### `writeError` and `writeJSON`

The switch is `errors.As` for `ValidationError`, then `errors.Is` for `ErrNotFound`, then `errors.As` for `ConflictError`, then the default.

The default logs the real error with method and path, and writes `{"error":"internal error"}`. `TestInternalErrorHidesCause` forces the store to return `dial tcp secret-host:3306` and asserts the body does not contain `secret-host`.

`writeJSON` sets `Content-Type: application/json`, writes the status, then encodes. `json.Encoder` adds a trailing newline. The tests that compare a full body include that newline. A failure while writing is logged. The status has already been sent, so the handler cannot change it.

`DELETE` does not use `writeJSON`. The 204 body is empty. Postman asserts the body equals `""`.

---

## `internal/device/mysql/repo.go`

### Statements

```go
const insertSQL = `INSERT INTO devices (id, name, brand, state) VALUES (?, ?, ?, ?)`
const updateSQL = `UPDATE devices SET name = ?, brand = ?, state = ? WHERE id = ?`
const deleteSQL = `DELETE FROM devices WHERE id = ?`
const selectByIDSQL = `SELECT id, name, brand, state, created_at FROM devices WHERE id = ?`
const selectForUpdateSQL = selectByIDSQL + ` FOR UPDATE`
```

`insertSQL` omits `created_at` so the default fills it. `updateSQL` omits it so the column cannot change through this statement. `TestWritesDoNotTouchCreationTime` asserts both, and asserts that the select still reads the column. The comment on `updateSQL` says the same thing for a reader of the code.

All values are `?` arguments. `TestListQueryUsesPlaceholders` builds a brand `O'Malley` and asserts the quote never appears in the SQL text. The filter values travel in the args slice. That is the injection boundary.

### `Insert`

Begins a transaction, inserts, selects the same id, commits. Returns the scanned row. `defer tx.Rollback()` covers a failed insert, a failed select, and a failed commit. A failed select does not leave a row behind. That is the comment on the method.

The service has already chosen the id. The repository does not generate one.

### `Get`

Scans one row. `sql.ErrNoRows` becomes `device.ErrNotFound`. Any other error is returned as-is and becomes a 500.

### `List`

`listQuery` builds the statement.

- No filters: `SELECT ... FROM devices ORDER BY created_at ASC, id ASC`. No `WHERE`.
- Brand only, state only, or both. Filters are appended in that order and joined with `AND`.
- Arguments line up with the `?` marks in that same order.

`out` starts as an empty slice, not nil. Zero rows encode as `[]`.

`rows.Err()` is returned after the loop. A scan error returns immediately. `defer rows.Close()` runs on both paths.

### `sqlTx`

`GetForUpdate` uses the `FOR UPDATE` statement and maps no-rows to `ErrNotFound`.

`Update` and `Delete` call `rowsAffected`. Zero rows is `ErrNotFound`. Under the lock this is a backstop: the row was just read, and the id written back is the locked id. A driver that cannot report a count returns that error upward, which is a 500. MySQL reports the count.

`Rollback` turns `sql.ErrTxDone` into nil so the `defer` after a successful commit is quiet. Any other rollback error is returned, and the caller of `Rollback` in the service ignores it, because it is a `defer`. The original error is the one the request sees.

### `scanDevice`

Scans id, name, brand, state, created_at. The state column is scanned into a plain string and then converted to `device.State`, because `database/sql` does not know the named type. `CreatedAt` is forced to UTC.

`scanner` is a small interface with `Scan`. Both `*sql.Row` and `*sql.Rows` have `Scan`, so one function reads a single row and a list row.

---

## `internal/notify/hub.go`

`subscriberBuffer` is 8. The comment says it is small on purpose.

`StateChanged` is the event payload. The JSON tags are the SSE `data` object.

`Hub` is a mutex and `map[chan StateChanged]struct{}`. The map is a set. The channel is the key.

`Subscribe`:

- Makes a buffered channel.
- Inserts it under the lock.
- Returns the receive-only end and a cancel function.
- `sync.Once` makes a second cancel a no-op. The stream handler defers cancel, and a test calls cancel twice on purpose (`TestUnsubscribeStopsDelivery`).

The channel is not closed. The reason is in the events handler section: the receive loop must not see a closed channel.

`Publish`:

- Holds the lock for the whole fan-out. Subscribers are few. The work inside the lock is a non-blocking send.
- `select` with `default` drops the event for a full buffer.
- An empty map sends nothing. `TestPublishWithNoSubscribers` covers that.

`Publish` can run while a request is in `Commit`'s caller, on the request goroutine. It does not start a goroutine and it does not do I/O. That is why a subscriber cannot slow the save.

The lock is not held across the HTTP write. The handler reads the channel outside the hub's lock and writes the socket there. A slow socket blocks that subscriber's handler, fills its buffer of 8, and then further events for that client are dropped. Other clients still receive them.

---

## `internal/id/uuid.go`

`uuidV4` is a case-insensitive pattern: 8-4-4-4-12 hex, the version nibble is `4`, the variant nibble is `8`, `9`, `a`, or `b`. That is version 4 and the RFC 4122 variant.

`New`:

- Reads 16 bytes from `crypto/rand`.
- Sets the version: `b[6] = (b[6] & 0x0f) | 0x40`, which forces the high nibble to 4 and keeps the low nibble random.
- Sets the variant: `b[8] = (b[8] & 0x3f) | 0x80`, which forces the top two bits to `10`.
- Hex-encodes and inserts the hyphens.

`Validate` returns `errors.New("invalid uuid")`. Callers in the service throw that error away and return `ValidationError` with `invalid device id`. The client never sees the word "uuid" from `Validate` itself. The path message is the stable one.

`TestNewIsUUIDv4` generates 1000 ids, checks the version nibble and the variant bits, and checks there are no duplicates. `TestValidateRejects` rejects empty, `events`, a version-1 UUID, a version-4 UUID with the wrong variant, and a string that is one character short.

The test id used everywhere else is `11111111-1111-4111-8111-111111111111`. The `4` and the `8` are in the positions the regex requires. The handler tests pin `newID` to that value so the JSON is deterministic.

---

## `migrations/001_devices.sql`

```sql
CREATE TABLE devices (
    id         CHAR(36)     NOT NULL,
    name       VARCHAR(255) NOT NULL,
    brand      VARCHAR(255) NOT NULL,
    state      ENUM('available', 'in-use', 'inactive') NOT NULL,
    created_at DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_devices_brand (brand),
    KEY idx_devices_state (state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
```

- `CHAR(36)` matches the hyphenated UUID, fixed width.
- `VARCHAR(255)` matches the rune limit.
- `ENUM` is the backstop for state.
- `DATETIME(6)` stores microseconds and does not auto-update on `UPDATE`, because there is no `ON UPDATE` clause. The Go `UPDATE` also omits the column. Both are required. An `ON UPDATE CURRENT_TIMESTAMP` would move the creation time on every rename even though the `UPDATE` statement forgot the column. It is not there.
- Primary key on `id`. InnoDB clusters on it.
- Secondary keys on `brand` and `state` for the filters. No key on `created_at`. The order-by can sort the matching rows. The table is the size of a demo.
- `utf8mb4_bin` is the case-sensitive brand comparison.
- `InnoDB` is required for `FOR UPDATE` row locks and for transactions. MyISAM would make the lock a no-op and the in-use rule would race.

The file is applied by the MySQL image, not by the Go process. There is no `Down` migration. Reset is `docker compose down -v`.

---

## `Dockerfile`

```dockerfile
FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.24
RUN apk add --no-cache ca-certificates
COPY --from=build /out/api /usr/local/bin/api
USER nobody
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
```

Multi-stage so the running image does not contain the compiler, the module cache, or the source.

`go.mod` and `go.sum` are copied first. `go mod download` is its own layer. A source change does not re-download modules.

`CGO_ENABLED=0` produces a static binary. The build image is glibc. Alpine is musl. A cgo binary built on the first would not run on the second. Static linking removes that split. `GOOS=linux` matters when someone builds the image on a Mac.

`-trimpath` drops local paths from the binary. `-s -w` strips the symbol table and DWARF. The image is smaller. Stack traces in a panic have less path detail. That is acceptable for this service.

The runtime image installs CA certificates and nothing else. The local DSN does not use TLS. The certificates are there so a later TLS DSN works, and so the base image is a normal runtime rather than a bare Alpine with no trust store.

`USER nobody` means the process is not root. The binary is owned by root and world-executable, so `nobody` can run it. It does not need to write a file. Logs go to stderr.

`EXPOSE` documents 8080. Compose is what publishes the port.

`.dockerignore` excludes `.git`, `*.pdf`, `*.md`, `bin`, `api`, and `postman`. The image build does not see the challenge PDF, this report, or the Postman collection. Those are not needed to compile, and the PDF should not land in an image layer.

---

## `docker-compose.yml`

Project name `devices-api`. Container names look like `devices-api-api-1` and `devices-api-mysql-1`.

### MySQL

Image `mysql:26.10.0`.

Environment: database `devices`, user `devices`, password `devices`, root password `root`. Those values are for this local stack. They are in the compose file because the stack has to start with zero setup. They are not a credential design.

`TZ=UTC` and `command: ["--default-time-zone=+00:00"]` make `CURRENT_TIMESTAMP(6)` write UTC.

The data volume is `mysql_data`. The SQL file is mounted read-only into `/docker-entrypoint-initdb.d/`. The entrypoint runs it only when the volume is empty.

The healthcheck is `mysqladmin ping` as root, every 5 seconds, 5 second timeout, 20 retries, 30 second start period. It proves the server accepts connections. It does not prove the `devices` table exists. If a volume was created by an earlier start that did not mount this SQL file, the ping is green and the API returns 500 until `down -v`.

MySQL has no `ports:` entry. It is reachable as `mysql:3306` on the compose network and not on the host. The reviewer does not need a local MySQL, and nothing else on the machine can collide with 3306.

Inspect rows with:

```bash
docker compose exec mysql mysql -udevices -pdevices devices \
  -e 'SELECT id, name, brand, state, created_at FROM devices'
```

### API

Built from the `Dockerfile`. Publishes `8080:8080`.

`DB_DSN` is `devices:devices@tcp(mysql:3306)/devices?parseTime=true&loc=UTC&charset=utf8mb4`.

- `parseTime=true` is required. Without it the driver returns `created_at` as `[]byte` and `scanDevice` cannot scan into `time.Time`.
- `loc=UTC` matches the server zone.
- `charset=utf8mb4` matches the table.

`depends_on` waits for the MySQL healthcheck. The process also retries for 30 seconds. Both are there.

`HTTP_ADDR=:8080` is explicit even though that is the code default.

### Why Docker is here

The assignment says the solution must be containerized and must use a real database. Compose is the way a reviewer runs that without installing Go or MySQL:

```bash
docker compose up --build
```

The API is on `http://localhost:8080`. `go test ./...` does not start these containers. The host Go toolchain is only for the unit tests and for people who want to compile outside Docker. The running API the reviewer hits is the binary inside the image, built with the pinned Go version.

`docker compose down -v` stops the stack and deletes the data volume. That is also how the schema is reapplied.

OrbStack and Docker Desktop both provide this CLI. The compose file does not depend on which one is installed.

---

## Postman and Newman

### Why this exists

`go test ./...` never opens a socket and never talks to MySQL. It is the fast check of the rules, the JSON shape, the SQL strings, the UUID bits, and the hub.

The assignment also says the service has to run, with a real database. Somebody has to send the HTTP and look at the status codes. Postman is that check, saved as a file so it is repeatable.

A collection is better than a page of `curl` commands for this suite because:

- The requests are ordered. Create saves `deviceId` and `createdAt`. Later requests use them. A second create saves `otherDeviceId`. That is the stateful part of the API, exercised the way a client exercises it.
- Each request asserts the status and the error string, not only "the command ran".
- The same file runs in the Postman app, for a live demo, and in Newman, for one terminal command.
- `baseUrl` lives in an environment, `Devices API Local`, value `http://localhost:8080`. The collection can point at another host without editing requests. The collection also has its own `baseUrl` variable with the same value, so a run that forgets the environment still hits the local stack.

Newman command, from the README:

```bash
npx newman run postman/devices-api.postman_collection.json \
  -e postman/local.postman_environment.json
```

Newman is the Postman collection runner. `npx` runs it without adding it to `go.mod` or to the image. `.dockerignore` excludes `postman`, so the collection is a client asset. It is not part of the server.

### The file to run

`postman/devices-api.postman_collection.json` is the file the README runs. It has 46 requests.

`postman/Devices API.postman_collection.json` is a Postman app export of the same suite. It carries Postman cloud ids. Its "List every device" request also contains a Postman visualizer: a chunk of HTML and Chart.js that the Postman UI can draw. That HTML is not the API. The API still returns a JSON array. The visualizer reads that array inside Postman. Do not describe the visualizer as part of the service. If it is on screen, say it is a Postman display attached to the exported copy, and that Newman runs the other file.

The blank-brand request in the Newman file calls `GET /devices?brand=%20`. `%20` is a space. The service trims it and returns `400 brand filter cannot be empty`.

### How the suite is arranged

Collection variables: `baseUrl`, `deviceId`, `createdAt`, `otherDeviceId`.

Folders, in the order a full run executes them:

1. **Health.** `GET /healthz` is 200 and `{"status":"ok"}`.
2. **Reject invalid input.** These run before any device is created. They cover the empty body, invalid JSON, a client `created_at`, a client `id`, missing name, blank name, missing brand, missing state, an unknown state, a name over 255 characters, a brand over 255 characters, an id that is not a UUID, an unknown UUID (`404`), a bad state filter, a blank brand filter, `PUT /devices` (`405` and the plain-text method-not-allowed body), an empty patch, a patch of all nulls, and a full update of an invalid id.
3. **Create and read.** Creates a Pixel 8 / Google / available. Asserts a version-4 id and a `created_at` of the form `YYYY-MM-DDTHH:MM:SS.ssssssZ`. Saves both. Fetches the device and compares `created_at` to the saved value. Lists devices and expects the id. Filters `brand=Google&state=available` and expects the id. Filters `brand=google` and expects the id to be absent.
4. **Update.** Patches the name to Pixel 9 and asserts brand, state, and `created_at` stayed. Rejects `{}`, rejects a `created_at` on update, rejects a full replace that omits state.
5. **In-use rules.** Patches the state to `in-use` and asserts the creation time is unchanged. Sends the same name while in use and expects 200. Patches a different brand and expects 409 with the rename message. Sends a full rename and expects the same 409. Deletes and expects 409 with `in-use devices cannot be deleted`. Patches the state to `inactive` without renaming and expects 200. Replaces name, brand, and state back to Pixel 8 / Google / available and asserts the id and creation time.
6. **Filters.** Creates an iPhone / Apple / inactive and saves `otherDeviceId`. `brand=Google&state=available` matches the first id and not the second. `brand=Apple&state=inactive` matches the second and not the first. `brand=Apple&state=available` matches neither. That is the `AND`.
7. **Delete.** Deletes the available device and expects 204 with an empty body. Fetches it and expects 404. Deletes it again and expects 404. Deletes the second device. Lists and expects both ids to be gone.
8. **Events.** One request, "Stream state changes".

### Why the stream request is skipped

The pre-request script is:

```javascript
if (pm.execution && typeof pm.execution.skipRequest === "function") {
    pm.execution.skipRequest();
}
```

A collection run and Newman would otherwise send `GET /devices/events` and wait until the client gives up. The response never ends. `skipRequest` is present on those runners, so the request is skipped and the run finishes.

Pressing Send on that request alone still opens the stream. `skipRequest` does not apply to that path in the way the script is written for a manual send. The description on the request says to leave it open and change a device from another request. You should see:

```text
: connected

event: device_state_changed
data: {"device_id":"...","previous_state":"available","new_state":"in-use"}
```

A name-only update, a patch that sets the current state, and a delete add no event. Events are not stored, so the stream only shows changes that happen while it is connected.

The test script on that request checks status 200, `Content-Type` containing `text/event-stream`, and the body containing `: connected`. Those assertions run when you send it in a way that completes. The automated run never reaches them, because it skips the request first.

`curl -N localhost:8080/devices/events` is the same demo without Postman. `-N` disables curl's buffer so events print as they arrive.

### What Postman does not prove

- It does not prove the `FOR UPDATE` race. Two parallel requests need a test that starts them together. The unit test proves the rule on one row. The lock is visible in the SQL string. A concurrency test against MySQL is not in the suite. Say that if they ask. The design is the lock. The automated proof of the lock under two sessions is a gap.
- It does not prove that a committed state change arrives on the stream. The runner skips that request. The handler test `TestStateChangeReachesSubscriber` proves it in process, with the real handler and the real hub, and a fake store.
- It does not prove a failed commit publishes nothing. `TestFailedWriteDoesNotNotify` does, with the fake store.

Use that split in the interview. Unit tests own the rules and the failure paths. Postman owns "a real MySQL and the real binary speak this HTTP."

---

## The tests, and what each one is for

`go test ./...` needs Go 1.27.3 on the host if you run it outside Docker. It does not need a database. Every test that can calls `t.Parallel()`.

### Service tests (`service_test.go`)

The id generator returns `sampleID`. The publisher is a `recorder` that appends events under a mutex. The store is `memStore`.

| Test | What it locks in |
| --- | --- |
| `TestCreateStoresDeviceAndDoesNotNotify` | Trims name and brand, stores the fixed id, gets a non-zero `created_at`, publishes nothing |
| `TestCreateValidation` | Missing and blank name, missing brand, missing and bad state, 256-character name. Nothing is stored. Nothing is published |
| `TestCreateAllows255Runes` | 255 runes of `á` is legal |
| `TestInUseRules` | Rename while in use does not update, commit, delete, or publish, and the stored name stays. The same name and brand can change state, creation time stays, one event is published. Delete while in use does not delete or publish |
| `TestPartialUpdateAndNoop` | A name patch leaves brand, state, and creation time, and publishes nothing. Patching the same name again does not call `Update` |
| `TestReplaceRequiresFields` | A replace with no state is the invalid-state error |
| `TestPatchRequiresAField` | An empty patch writes nothing |
| `TestFailedWriteDoesNotNotify` | An update error and a commit error both publish nothing and leave the stored state at `available` |
| `TestDeleteAvailableDoesNotNotify` | An available device is removed and nothing is published |
| `TestListFilters` | Order is creation time. Brand and state together match one row. A bad state and a blank brand are validation errors. A missing brand returns an empty non-nil slice |
| `TestGetNotFoundAndInvalidID` | Unknown id is `ErrNotFound`. The id `events` is `invalid device id` for get and for delete |

`assertValidation` and `assertConflict` use `errors.As` and compare the message. A handler that returned the right sentence with the wrong type would still fail these tests. That matters because the HTTP status comes from the type.

### The in-memory store (`mem_test.go`)

This is not a second database. It is a fake `Store`.

`Insert` returns an error when `insertErr` is set. When `CreatedAt` is zero it assigns `2026-10-27T15:04:05.123456Z`. The MySQL repository reads the clock back from the database. The fake pins one timestamp so handler tests can look for that exact string.

`List` filters in Go, then sorts by creation time and id. That matches `ORDER BY created_at ASC, id ASC`.

`memTx` holds the update or the delete aside until `Commit`. `Update` copies the device into `pending`. `Delete` records the id. `Commit` applies them to the map unless `commitErr` is set. `Rollback` drops them. A failed commit in `TestFailedWriteDoesNotNotify` leaves the map as it was, which is what a rolled-back MySQL transaction does.

The fake does not implement real locking across goroutines beyond a mutex around the map. Service tests do not run two mutations on one store at the same time. Do not claim the fake proves the race. It proves the service's branching.

### Handler tests (`handler_test.go`)

`newHandler` builds a real hub, a real service, and the fixed id function, over the fake store.

| Test | What it locks in |
| --- | --- |
| `TestCreateAndFetch` | 201 body contains the id, the trimmed fields, and the pinned timestamp. Get returns the name. The brand-and-state filter returns the id. `?brand=Apple` is `[]\n` |
| `TestRejectedRequests` | Client `created_at`, empty body, broken JSON, a bad id, a missing device, a bad state filter, rename while in use by patch and by put, delete while in use, empty patch. Afterwards the store's update count and delete count are still 0 |
| `TestUpdateDeleteAndMethod` | Patch of state keeps `created_at`. Put changes the name. Delete is 204 with an empty body and the store then returns not found. `PUT /devices` is 405 |
| `TestInternalErrorHidesCause` | A store error that contains a host name becomes exactly `{"error":"internal error"}` |
| `TestHealthAndEmptyList` | Health JSON, and a list of nothing is `[]` |
| `TestStateChangeReachesSubscriber` | The stream content type, the `: connected` comment, one event for available to in-use, no extra event for a same-state patch, one more event for the move back to available, and no event for the delete. The count of `device_state_changed` ends at 2 |
| `TestBodyTooLarge` | A body over 1 MiB is 400 and the message contains `too large` |

`TestStateChangeReachesSubscriber` is the one to walk. It creates a device, starts the stream handler on a goroutine with a cancelable context, waits until the recorder is flushed, patches the state, waits for another flush, and checks the SSE text. It then sends a no-op, moves the device off `in-use` so delete is legal, consumes that second event, deletes, and asserts the event count did not grow. `t.Cleanup` cancels the context and waits for the handler goroutine, so the test does not leak it.

`sseRecorder` exists because `httptest.ResponseRecorder` can flush, and it does not tell the test when it did. This recorder implements `http.Flusher`, pushes on a buffered channel in `Flush`, and locks around the buffer. `waitFlush` fails the test after 2 seconds.

### SQL tests (`mysql/query_test.go`)

No database. `TestListQueryUsesPlaceholders` rejects interpolation of `O'Malley` and checks the `ORDER BY`. `TestListQueryFilters` checks the unfiltered query, brand only, and state only. `TestWritesDoNotTouchCreationTime` checks the statement constants.

### Hub tests

`TestPublishReachesEverySubscriber` uses two subscribers. `TestUnsubscribeStopsDelivery` cancels twice and checks the map length is 1. `TestPublishDoesNotBlockWhenBufferIsFull` publishes `subscriberBuffer + 5` events from goroutines and fails if any publish takes a second. It then drains without blocking and expects exactly 8 events. `TestPublishWithNoSubscribers` publishes on an empty hub.

### `cmd/api/main_test.go`

One test. The logging wrapper records 204 and forwards `Flush`. That test is small because the wrapper is the difference between a working stream and a handler that reports "streaming is not supported" behind the logger.

---

## Status codes, as one table you can redraw

| Code | When |
| --- | --- |
| 200 | Get, list, put, patch, health, and the stream opening |
| 201 | Create |
| 204 | Delete |
| 400 | Bad JSON, unknown field, validation, bad id, bad filter, empty patch, body too large, a second JSON value |
| 404 | No row for that id |
| 405 | A method the mux does not register, such as `PUT /devices` |
| 409 | In-use rename, or delete of an in-use device |
| 500 | Unexpected error. Body is `internal error`. Cause is in the log |

Errors the handlers write are `{"error":"message"}`. The 405 body is plain text from `net/http`.

---

## Things they can poke at, and the answer

**Why is the brand filter case-sensitive?** The column collation is `utf8mb4_bin`. The Postman request that uses `google` against a stored `Google` asserts the id is absent.

**Why can I set an in-use device to inactive and rename it in one request? You cannot.** `validateChange` looks at the current state. Leaving `in-use` is allowed only when the new name and brand equal the stored ones. Renaming an `available` device into `in-use` in one request is allowed, because the current state is not `in-use`.

**What if two deletes race an update?** They take the same row lock. The second runs against whatever the first committed.

**What if the process dies after commit and before publish?** The subscriber misses that event. The assignment says recovery is not required. The row is still correct. Say the outbox sentence if they want the production version.

**What if a client connects after the change?** They see nothing for that change. The hub does not remember it. The first bytes they see are `: connected`.

**What if the subscriber is slow?** Eight events buffer, then that client skips, and the save returns.

**Why is `healthz` green when MySQL is down?** It is a process check. Device routes return 500. Readiness would be a separate check.

**Why does the list return every row?** Pagination was left out. The order is already a cursor: `(created_at, id)`.

**Why UUID v4 in the primary key?** The id is the public identifier and the standard library can generate v4 with `crypto/rand`. The table is small. A large table would want a time-ordered key.

**Why is creation time a `DATETIME` and not a `TIMESTAMP`?** `DATETIME(6)` stores the clock value with microseconds and does not rewrite it on read. The session time zone is pinned to UTC so the default is UTC at insert. There is no `ON UPDATE`.

**Why does a no-op patch return 200?** The resource is already in the requested state. The response is the current device. No write, no event.

**Why is `{}` a 400 and a same-value patch a 200?** `{}` names no field. A patch that names the current name is a real request whose result happens to match the row.

**Does the service trim the brand filter?** It rejects a filter that is empty after trimming. It does not trim a filter that has other characters. Stored brands are trimmed at write time. The filter is exact.

**Where is the password?** In the compose file, for the local stack, user `devices`, password `devices`, database `devices`, root password `root`. MySQL is not published on the host.

**Can two API containers share the stream?** No. Each has its own hub. They can share MySQL and they will not see each other's events.

**What does the access log of a stream look like?** One line, written when the client disconnects, status 200, duration equal to how long the stream was open.

---

## A path through the demo

1. `docker compose up --build`
2. `curl -sS localhost:8080/healthz`
3. Create a device with the `curl` in the README. Show the server-assigned id and `created_at`.
4. In another terminal, `curl -N localhost:8080/devices/events`. Show `: connected`.
5. `PATCH` the state to `in-use`. Show the SSE event and show that `created_at` in the PATCH response did not move.
6. `PATCH` the name. Show the `409` body. Show that the stream printed nothing.
7. `DELETE`. Show the `409` body.
8. `PATCH` the state to `available`. Show a second event.
9. `DELETE`. Show `204` and show that the stream printed nothing.
10. If they want the suite: `go test ./...`, then the Newman command. Mention that Newman skips the stream request and that step 4 is how you see it.

---

## Short index of files

| File | One sentence |
| --- | --- |
| `cmd/api/main.go` | Opens MySQL, retries, serves HTTP, logs the status, shuts down on a signal |
| `internal/device/device.go` | The device type, the three states, the error messages, the JSON timestamp |
| `internal/device/service.go` | Validation, the row lock, the in-use rule, publish-after-commit |
| `internal/device/handler.go` | Status codes, JSON errors, the SSE loop |
| `internal/device/repository.go` | The two interfaces the service needs |
| `internal/device/mysql/repo.go` | Parameterized SQL, creation time left to MySQL, `FOR UPDATE` |
| `internal/notify/hub.go` | In-memory fan-out that drops instead of blocking |
| `internal/id/uuid.go` | Generate and check UUID v4 |
| `migrations/001_devices.sql` | The table, applied once on an empty volume |
| `Dockerfile` | Static binary, Alpine, user `nobody` |
| `docker-compose.yml` | MySQL and the API, UTC, MySQL not published on the host |
| `postman/devices-api.postman_collection.json` | The HTTP contract Newman runs |
| `postman/local.postman_environment.json` | `baseUrl` = `http://localhost:8080` |
