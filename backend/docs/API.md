# API (v1)

Base path `/api/v1`. JSON in, JSON out. Persian messages in `error.message`; branch on `error.code`.
Every response has `X-Request-Id`; quote it when reporting a problem.

## Conventions

- **Auth:** `Authorization: Bearer <access_token>` on everything except `auth/login` and `auth/refresh`.
- **Content-Type** must be `application/json` for POST/PUT/PATCH; unknown JSON fields are rejected (`bad_json`).
- **Errors:** `{"error": {"code": "...", "message": "...", "details": ...}, "request_id": "..."}`
- **Idempotency:** send a fresh UUID as `op_id` on every state‑changing request. If the network drops, send the
  *same* request again: the original result comes back with header `Idempotent-Replay: true` and nothing is applied twice.
- **Tent codes** are `CH-000123`; Persian digits and lowercase are accepted.
- **Rate limit:** 60 requests/second per session (`429` + `Retry-After`).
- **Dates:** birth date accepts `1370/05/12` (Jalali) or `1991-08-03`.

| status | meaning |
|---|---|
| 400 | malformed request (`bad_json`, `bad_tent_code`, ...) |
| 401 | not logged in / token expired → refresh or log in |
| 403 | role not allowed, or `password_change_required` |
| 404 | not found |
| 409 | business conflict: `shift_required`, `tents_unavailable` (with `details` listing each tent), `invalid_transition`, ... |
| 422 | valid JSON but invalid data: `bad_national_id`, `identity_mismatch`, `pilgrim_unverified` |
| 429 | rate limited / account temporarily locked |

## Session

| | |
|---|---|
| `POST /auth/login` `{username, password}` | → `{tokens:{access_token, refresh_token, expires_at}, user}` |
| `POST /auth/refresh` `{refresh_token}` | single-use; returns a new pair |
| `POST /auth/logout` | |
| `POST /auth/change-password` `{old_password, new_password}` | ≥ 10 characters; all sessions are revoked, log in again |
| `GET /me` | role, current shift |
| `GET /places` | gates (with doors), storage room, workshop |
| `POST /shifts/start` `{place_id, door_id?}` | required before any operational call |
| `POST /shifts/end` | |

## Pilgrims

| | |
|---|---|
| `POST /pilgrims` `{national_id, birth_date, mobile}` | registers (201) or finds (200, `existing:true`); runs the identity inquiry. `verification` is `verified` or `pending` (inquiry service down). |
| `POST /pilgrims/lookup` `{national_id}` or `{mobile}` | POST so personal data never appears in a URL |
| `GET /pilgrims/{id}` | masked view + `active_tents` |

## Tents (the hot path)

| | |
|---|---|
| `GET /tents/{code}` | scan result: status, place, and — if with a pilgrim — who and since when |
| `GET /tents/{code}/history?limit=50` | every event, newest first |
| `POST /assignments` `{pilgrim_id, codes[], socks_qty?, op_id?}` | **all or nothing**; up to 20 tents. 409 `tents_unavailable` lists which ones and why. |
| `POST /returns` `{items:[{code, status?}], op_id?}` | `status` = `ready` (default) / `dirty` / `repair`. **Per item:** one bad scan does not fail the batch. `completed_pilgrims` counts pilgrims who now hold nothing (they get the confirmation SMS). |
| `POST /assignments/cancel` `{code, op_id?}` | undo a hand-out. Servers: own, within `UNDO_WINDOW`. Gate lead/office/admin: any time. |
| `POST /tents/status` `{code, to, note?, op_id?}` | allowed transitions depend on role (see `domain/status.go`) |
| `POST /tents/move` `{codes[], to_place_id, op_id?}` | physical transfer between gate/storage/workshop |
| `POST /retire-requests` `{code, reason: lost\|damaged\|other, note?}` | gate lead / team / office / admin |
| `GET /retire-requests` · `POST /retire-requests/{id}/decision` `{approve}` | office / admin; cannot approve your own request unless admin |

Tent statuses: `ready`, `with_pilgrim`, `dirty`, `in_wash`, `pending_review`, `repair`, `retired`.

## Socks, handover

| | |
|---|---|
| `POST /socks` `{kind: supply\|issue\|return, qty, place_id?, pilgrim_id?, op_id?}` | `supply` = office → gate; `issue` = gate → pilgrim; `return` = leftover back. Never goes negative. (Socks sold together with tents: use `socks_qty` in `/assignments`.) |
| `POST /handovers` `{to_user, notes?}` | snapshot of the place (tents by status, socks balance, tents out) |
| `GET /handovers` · `POST /handovers/{id}/resolve` `{accept}` | only the receiver can resolve |

## Offline sync

`POST /sync` `{ops:[{op_id, type, shift_id, client_ts, payload}]}` — up to 200 ops, applied in order.
`type` ∈ `assign | return | cancel | status | move | socks`; `payload` is the same body as the endpoint above.
Result per op: `ok`, `replayed` (already applied), `rejected` (a business rule said no — do not resend),
or `retry` (temporary failure — keep it queued). `shift_id` attributes the op to the shift it was recorded in
(must be the caller's own shift, ended less than 48 h ago). Registering a *new* pilgrim needs connectivity
(identity inquiry); everything after that can be queued.

## Live view

| | |
|---|---|
| `GET /dashboard` | counts by status and place, today's totals, overdue. Gate roles only get their own place. Cached 2 s. |
| `GET /events` | server-sent events (`assigned`, `returned`, `status`, `moved`, `cancelled`, `retired`). Browsers cannot set headers on `EventSource`; read it with `fetch()` + a stream reader. The stream ends when the access token would expire — reconnect with a fresh one. |
| `GET /reports/daily?date=YYYY-MM-DD[&format=xlsx]` | office/admin; Persian right-to-left workbook |

## Admin (`role=admin`)

`GET/POST /admin/users` · `PATCH /admin/users/{id}` `{full_name?, role?, active?}` ·
`POST /admin/users/{id}/reset-password` · `POST /admin/tents` `{codes[]}` or `{from, to}` ·
`POST /admin/tents/replace-label` `{old_code, new_code}` · `POST /admin/places` `{kind, name}` ·
`PATCH /admin/places/{id}` `{active}` · `POST /admin/places/{id}/doors` `{name}` ·
`GET /admin/settings` · `PUT /admin/settings/{key}` (`socks_unit_price`, `max_tents_per_request`, `sms_templates`) ·
`GET /admin/audit?before=&limit=`

Temporary passwords are returned **once** (create / reset). Show them to the administrator and never log them.

## Ops endpoints (no auth)

`GET /healthz` (process is up) · `GET /readyz` (database and Redis answer).
Prometheus metrics are on a separate internal port (`METRICS_ADDR`), never on the public one.
