CREATE TABLE recalls (
    id              text PRIMARY KEY,
    event_id        text NOT NULL DEFAULT '',
    firm            text NOT NULL,
    product         text NOT NULL,
    reason          text NOT NULL DEFAULT '',
    classification  text NOT NULL DEFAULT '',
    status          text NOT NULL DEFAULT '',
    distribution    text NOT NULL DEFAULT '',
    code_info       text NOT NULL DEFAULT '',
    gtins           text[] NOT NULL DEFAULT '{}',
    gtins_inferred  boolean NOT NULL DEFAULT false,
    spec            jsonb NOT NULL DEFAULT '{}',
    initiated_on    date,
    reported_on     date,
    source          text NOT NULL DEFAULT '',
    content_hash    text NOT NULL,
    ingested_at     timestamptz NOT NULL DEFAULT now(),
    seq             bigserial
);
-- "Which recalls mention this barcode?" (used when a new lot is received).
CREATE INDEX recalls_gtins_idx ON recalls USING gin (gtins);

CREATE TABLE lots (
    id           bigserial PRIMARY KEY,
    sku          text NOT NULL DEFAULT '',
    name         text NOT NULL,
    brand        text NOT NULL DEFAULT '',
    gtin         text NOT NULL DEFAULT '',
    lot_code     text NOT NULL DEFAULT '',
    best_by      date,
    quantity     integer NOT NULL CHECK (quantity >= 0),
    location     text NOT NULL DEFAULT '',
    received_on  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX lots_gtin_idx ON lots (gtin) WHERE gtin <> '';

CREATE TABLE alerts (
    id          bigserial PRIMARY KEY,
    recall_id   text NOT NULL REFERENCES recalls (id),
    lot_id      bigint NOT NULL REFERENCES lots (id),
    tier        text NOT NULL,
    score       double precision NOT NULL,
    reasons     text[] NOT NULL DEFAULT '{}',
    state       text NOT NULL DEFAULT 'new',
    version     integer NOT NULL DEFAULT 1,
    pulled_qty  integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (recall_id, lot_id)
);
CREATE INDEX alerts_state_idx ON alerts (state);

CREATE TABLE alert_events (
    id          bigserial PRIMARY KEY,
    alert_id    bigint NOT NULL REFERENCES alerts (id),
    action      text NOT NULL,
    from_state  text NOT NULL,
    to_state    text NOT NULL,
    actor       text NOT NULL,
    note        text NOT NULL DEFAULT '',
    quantity    integer NOT NULL DEFAULT 0,
    recipients  text[] NOT NULL DEFAULT '{}',
    at          timestamptz NOT NULL
);
CREATE INDEX alert_events_alert_idx ON alert_events (alert_id, id);
