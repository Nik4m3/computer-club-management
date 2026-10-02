-- +goose Up
-- +goose StatementBegin
CREATE TABLE admins
(
    id            BIGSERIAL PRIMARY KEY,
    login         VARCHAR(100) NOT NULL UNIQUE,
    password_hash TEXT         NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE EXTENSION IF NOT EXISTS pgcrypto;
INSERT INTO admins (login, password_hash)
VALUES ('admin', crypt('admin', gen_salt('bf', 10)))

CREATE TABLE clients
(
    id         BIGSERIAL PRIMARY KEY,
    full_name  VARCHAR(255) NOT NULL,
    phone      VARCHAR(20)  NOT NULL UNIQUE,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_clients_full_name ON clients (full_name);
INSERT INTO clients (full_name, phone)
VALUES
    ('Иванов Иван Иванович', '+79990000001'),
    ('Петров Пётр Петрович', '+79990000002'),
    ('Сидоров Алексей Сергеевич', '+79990000003'),
    ('Кузнецов Дмитрий Андреевич', '+79990000004'),
    ('Попов Максим Олегович', '+79990000005'),
    ('Смирнов Артём Игоревич', '+79990000006'),
    ('Васильев Никита Павлович', '+79990000007'),
    ('Морозов Кирилл Денисович', '+79990000008'),
    ('Новиков Егор Романович', '+79990000009'),
    ('Фёдоров Михаил Александрович', '+79990000010');

CREATE TABLE halls
(
    id          BIGSERIAL PRIMARY KEY,
    name        VARCHAR(100) NOT NULL UNIQUE,
    description TEXT,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
INSERT INTO halls (name, description)
VALUES
    ('Основной', 'Основной зал компьютерного клуба'),
    ('VIP', 'VIP-зал с игровыми ПК'),
    ('Консольный', 'Зал с игровыми консолями')

CREATE TABLE devices
(
    id         BIGSERIAL PRIMARY KEY,
    hall_id    BIGINT      NOT NULL,
    type       VARCHAR(20) NOT NULL,
    number     INTEGER     NOT NULL,
    name       VARCHAR(100),
    status     VARCHAR(20) NOT NULL DEFAULT 'available',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_devices_hall FOREIGN KEY (hall_id) REFERENCES halls (id) ON DELETE RESTRICT,
    CONSTRAINT uq_devices_hall_number UNIQUE (hall_id, number),
    CONSTRAINT chk_devices_type CHECK (type IN ('pc', 'console')),
    CONSTRAINT chk_devices_status CHECK ( status IN ('available', 'maintenance', 'offline')),
    CONSTRAINT chk_devices_number CHECK (number > 0)
);
CREATE INDEX idx_devices_hall_id ON devices (hall_id);
CREATE INDEX idx_devices_status ON devices (status);
INSERT INTO devices (hall_id, type, number, name, status)
SELECT h.id, d.type, d.number, d.name, d.status
FROM (VALUES ('Основной', 'pc', 1, 'PC-01', 'available'),
             ('Основной', 'pc', 2, 'PC-02', 'available'),
             ('Основной', 'pc', 3, 'PC-03', 'available'),
             ('Основной', 'pc', 4, 'PC-04', 'available'),
             ('Основной', 'pc', 5, 'PC-05', 'offline'),
             ('VIP', 'pc', 1, 'PC-06', 'available'),
             ('VIP', 'pc', 2, 'PC-07', 'available'),
             ('VIP', 'pc', 3, 'PC-08', 'available'),
             ('VIP', 'pc', 4, 'PC-09', 'maintenance'),
             ('VIP', 'pc', 5, 'PC-10', 'available'),
             ('Консольный', 'console', 1, 'PlayStation 5 #1', 'available'),
             ('Консольный', 'console', 2, 'PlayStation 5 #2', 'available'),
             ('Консольный', 'console', 3, 'PlayStation 5 #3', 'available'),
             ('Консольный', 'console', 4, 'PlayStation 5 #4', 'available'),
             ('Консольный', 'console', 5, 'PlayStation 5 #5', 'available')) AS d (hall_name, type, number, name, status)
         JOIN halls h
              ON h.name = d.hall_name
    ON CONFLICT (hall_id, number) DO UPDATE
                                         SET type       = EXCLUDED.type,
                                         name       = EXCLUDED.name,
                                         status     = EXCLUDED.status,
                                         updated_at = NOW();

CREATE TABLE sessions
(
    id             BIGSERIAL PRIMARY KEY,
    client_id      BIGINT      NOT NULL,
    device_id      BIGINT      NOT NULL,
    status         VARCHAR(20) NOT NULL DEFAULT 'reserved',
    duration_minutes INTEGER NOT NULL DEFAULT 0,
    actual_duration_minutes INTEGER,
    paused_minutes INTEGER NOT NULL DEFAULT 0,
    paused_at      TIMESTAMPTZ,
    started_at     TIMESTAMPTZ,
    planned_end_at TIMESTAMPTZ,
    ended_at       TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_sessions_client FOREIGN KEY (client_id) REFERENCES clients (id) ON DELETE RESTRICT,
    CONSTRAINT fk_sessions_device FOREIGN KEY (device_id) REFERENCES devices (id) ON DELETE RESTRICT,
    CONSTRAINT chk_sessions_status CHECK (status IN ('reserved', 'active', 'paused', 'finished', 'cancelled')),
    CONSTRAINT chk_sessions_dates CHECK ( planned_end_at IS NULL OR started_at IS NULL OR planned_end_at >= started_at),
    CONSTRAINT chk_sessions_ended CHECK ( ended_at IS NULL OR started_at IS NULL OR ended_at >= started_at)
);
CREATE INDEX idx_sessions_client_id ON sessions (client_id);
CREATE INDEX idx_sessions_device_id ON sessions (device_id);
CREATE INDEX idx_sessions_status ON sessions (status);
CREATE INDEX idx_sessions_planned_end_at ON sessions (planned_end_at);
CREATE UNIQUE INDEX uq_active_session_per_device ON sessions (device_id) WHERE status IN ('reserved', 'active', 'paused');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS devices;
DROP TABLE IF EXISTS halls;
DROP TABLE IF EXISTS clients;
DROP TABLE IF EXISTS admins;
-- +goose StatementEnd
