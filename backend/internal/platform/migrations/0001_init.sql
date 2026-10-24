-- Core schema. Invariants live in the database, not only in Go:
--   * at most one active assignment per tent (partial unique index)
--   * one open shift per user (partial unique index)
--   * audit_log and tent_events are append-only (trigger)

CREATE TYPE user_role AS ENUM ('admin', 'office', 'gate_lead', 'server', 'team', 'workshop', 'service');
CREATE TYPE place_kind AS ENUM ('gate', 'storage', 'workshop', 'repair');
CREATE TYPE tent_status AS ENUM ('ready', 'with_pilgrim', 'dirty', 'in_wash', 'pending_review', 'repair', 'retired');
CREATE TYPE verification AS ENUM ('verified', 'pending', 'failed');
CREATE TYPE socks_kind AS ENUM ('supply', 'issue', 'return');
CREATE TYPE request_state AS ENUM ('pending', 'approved', 'rejected');

CREATE TABLE places (
    id        smallserial PRIMARY KEY,
    kind      place_kind NOT NULL,
    name      text NOT NULL UNIQUE,
    active    boolean NOT NULL DEFAULT true
);

CREATE TABLE doors (
    id        smallserial PRIMARY KEY,
    place_id  smallint NOT NULL REFERENCES places(id),
    name      text NOT NULL,
    active    boolean NOT NULL DEFAULT true,
    UNIQUE (place_id, name)
);

CREATE TABLE users (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username             text NOT NULL UNIQUE,
    full_name            text NOT NULL,
    role                 user_role NOT NULL,
    password_hash        text NOT NULL,
    must_change_password boolean NOT NULL DEFAULT true,
    active               boolean NOT NULL DEFAULT true,
    created_at           timestamptz NOT NULL DEFAULT now(),
    created_by           uuid REFERENCES users(id),
    last_login_at        timestamptz
);

CREATE TABLE shifts (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id),
    place_id   smallint NOT NULL REFERENCES places(id),
    door_id    smallint REFERENCES doors(id),
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz
);
CREATE UNIQUE INDEX one_open_shift_per_user ON shifts (user_id) WHERE ended_at IS NULL;
CREATE INDEX shifts_place_started ON shifts (place_id, started_at DESC);

-- Personal data is stored encrypted (AES-GCM). *_idx columns are keyed HMAC "blind indexes"
-- so lookups and uniqueness work without decrypting anything.
CREATE TABLE pilgrims (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    nid_enc        bytea,
    nid_idx        bytea UNIQUE,
    birth_enc      bytea,
    mobile_enc     bytea,
    mobile_idx     bytea,
    name_enc       bytea,
    verification   verification NOT NULL DEFAULT 'pending',
    verified_at    timestamptz,
    created_by     uuid NOT NULL REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    anonymized_at  timestamptz
);
CREATE INDEX pilgrims_mobile_idx ON pilgrims (mobile_idx);
CREATE INDEX pilgrims_last_seen ON pilgrims (last_seen_at) WHERE anonymized_at IS NULL;

CREATE TABLE tents (
    id         serial PRIMARY KEY,
    code       text NOT NULL UNIQUE,
    status     tent_status NOT NULL DEFAULT 'ready',
    place_id   smallint REFERENCES places(id),
    kind       text NOT NULL DEFAULT 'standard',
    note       text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- a tent that is with a pilgrim has no place; every other status has none required
    CHECK (status <> 'with_pilgrim' OR place_id IS NULL)
);
CREATE INDEX tents_status_place ON tents (status, place_id);

CREATE TABLE assignments (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pilgrim_id         uuid NOT NULL REFERENCES pilgrims(id),
    tent_id            integer NOT NULL REFERENCES tents(id),
    assigned_by        uuid NOT NULL REFERENCES users(id),
    assigned_shift_id  uuid REFERENCES shifts(id),
    assigned_place_id  smallint REFERENCES places(id),
    assigned_door_id   smallint REFERENCES doors(id),
    assigned_at        timestamptz NOT NULL DEFAULT now(),
    returned_at        timestamptz,
    returned_by        uuid REFERENCES users(id),
    returned_shift_id  uuid REFERENCES shifts(id),
    returned_place_id  smallint REFERENCES places(id),
    returned_door_id   smallint REFERENCES doors(id),
    return_status      tent_status,
    cancelled_at       timestamptz,
    cancelled_by       uuid REFERENCES users(id),
    lost_at            timestamptz,
    reminders          smallint NOT NULL DEFAULT 0,
    last_reminded_at   timestamptz
);
CREATE UNIQUE INDEX one_active_assignment_per_tent
    ON assignments (tent_id) WHERE returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL;
CREATE INDEX active_assignments_by_pilgrim
    ON assignments (pilgrim_id) WHERE returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL;
CREATE INDEX active_assignments_by_age
    ON assignments (assigned_at) WHERE returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL;
CREATE INDEX assignments_assigned_at ON assignments (assigned_at);

CREATE TABLE tent_events (
    id            bigserial PRIMARY KEY,
    tent_id       integer NOT NULL REFERENCES tents(id),
    type          text NOT NULL,
    from_status   tent_status,
    to_status     tent_status,
    place_id      smallint,
    door_id       smallint,
    user_id       uuid NOT NULL,
    shift_id      uuid,
    pilgrim_id    uuid,
    assignment_id uuid,
    meta          jsonb,
    client_ts     timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tent_events_tent ON tent_events (tent_id, id DESC);
CREATE INDEX tent_events_created ON tent_events (created_at);

CREATE TABLE socks_ledger (
    id         bigserial PRIMARY KEY,
    kind       socks_kind NOT NULL,
    place_id   smallint REFERENCES places(id),
    shift_id   uuid,
    user_id    uuid NOT NULL REFERENCES users(id),
    pilgrim_id uuid REFERENCES pilgrims(id),
    qty        integer NOT NULL CHECK (qty > 0),
    unit_price bigint NOT NULL DEFAULT 0,
    amount     bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX socks_place_kind ON socks_ledger (place_id, kind);
CREATE INDEX socks_created ON socks_ledger (created_at);

CREATE TABLE handovers (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    place_id     smallint NOT NULL REFERENCES places(id),
    from_user    uuid NOT NULL REFERENCES users(id),
    to_user      uuid NOT NULL REFERENCES users(id),
    snapshot     jsonb NOT NULL,
    notes        text,
    state        text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'accepted', 'disputed')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    resolved_at  timestamptz
);

CREATE TABLE retire_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tent_id      integer NOT NULL REFERENCES tents(id),
    reason       text NOT NULL CHECK (reason IN ('lost', 'damaged', 'other')),
    note         text,
    state        request_state NOT NULL DEFAULT 'pending',
    requested_by uuid NOT NULL REFERENCES users(id),
    requested_at timestamptz NOT NULL DEFAULT now(),
    decided_by   uuid REFERENCES users(id),
    decided_at   timestamptz
);
CREATE UNIQUE INDEX one_pending_retire_per_tent ON retire_requests (tent_id) WHERE state = 'pending';

-- Replays of the same client operation (offline sync / retries) return the stored result.
CREATE TABLE client_ops (
    op_id      uuid PRIMARY KEY,
    user_id    uuid NOT NULL,
    kind       text NOT NULL,
    result     jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX client_ops_created ON client_ops (created_at);

CREATE TABLE audit_log (
    id        bigserial PRIMARY KEY,
    at        timestamptz NOT NULL DEFAULT now(),
    user_id   uuid,
    action    text NOT NULL,
    entity    text,
    entity_id text,
    ip        text,
    meta      jsonb
);
CREATE INDEX audit_at ON audit_log (at DESC);
CREATE INDEX audit_entity ON audit_log (entity, entity_id);

CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid
);

CREATE TABLE sms_log (
    id           bigserial PRIMARY KEY,
    dedupe_key   text NOT NULL UNIQUE,
    pilgrim_id   uuid,
    template     text NOT NULL,
    state        text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'sent', 'failed')),
    provider_ref text,
    error        text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    sent_at      timestamptz
);

-- Append-only guard. The application never needs UPDATE/DELETE on these tables.
CREATE FUNCTION forbid_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '42501';
END $$;

CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER tent_events_append_only BEFORE UPDATE OR DELETE ON tent_events
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER tent_events_no_truncate BEFORE TRUNCATE ON tent_events
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();
