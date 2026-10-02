package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type API struct {
	pool     *pgxpool.Pool
	sessions map[string]int64
	mu       sync.RWMutex
}

func NewAPI(pool *pgxpool.Pool) *API {
	a := &API{
		pool:     pool,
		sessions: make(map[string]int64),
	}

	go a.runSessionWorker(context.Background())

	return a
}

func (a *API) Routes() chi.Router {
	r := chi.NewRouter()

	r.Post("/auth/login", a.login)
	r.Post("/auth/logout", a.logout)
	r.Get("/auth/me", a.me)

	r.Group(func(r chi.Router) {
		r.Use(a.authMiddleware)

		r.Get("/dashboard", a.dashboard)

		r.Get("/halls", a.listHalls)
		r.Post("/halls", a.createHall)
		r.Put("/halls/{id}", a.updateHall)
		r.Delete("/halls/{id}", a.deleteHall)

		r.Get("/clients", a.listClients)
		r.Post("/clients", a.createClient)
		r.Put("/clients/{id}", a.updateClient)
		r.Delete("/clients/{id}", a.deleteClient)

		r.Get("/devices", a.listDevices)
		r.Post("/devices", a.createDevice)
		r.Put("/devices/{id}", a.updateDevice)
		r.Delete("/devices/{id}", a.deleteDevice)

		r.Get("/sessions", a.listSessions)
		r.Post("/sessions", a.createSession)
		r.Post("/sessions/{id}/start", a.startSession)
		r.Post("/sessions/{id}/pause", a.pauseSession)
		r.Post("/sessions/{id}/resume", a.resumeSession)
		r.Post("/sessions/{id}/extend", a.extendSession)
		r.Post("/sessions/{id}/transfer", a.transferSession)
		r.Post("/sessions/{id}/finish", a.finishSession)
	})

	return r
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}

	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	req.Login = strings.TrimSpace(req.Login)
	if req.Login == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "login and password are required")
		return
	}

	var adminID int64
	err := a.pool.QueryRow(r.Context(), `
SELECT id
FROM admins
WHERE login = $1
  AND password_hash = crypt($2, password_hash)`, req.Login, req.Password).Scan(&adminID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid login or password")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	a.mu.Lock()
	a.sessions[token] = adminID
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "cc_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   8 * 60 * 60,
	})

	w.WriteHeader(http.StatusNoContent)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("cc_session"); err == nil {
		a.mu.Lock()
		delete(a.sessions, cookie.Value)
		a.mu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "cc_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	w.WriteHeader(http.StatusNoContent)
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("cc_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	a.mu.RLock()
	adminID, ok := a.sessions[cookie.Value]
	a.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	writeJSON(w, http.StatusOK, map[string]int64{"admin_id": adminID})
}

func (a *API) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("cc_session")
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		a.mu.RLock()
		_, ok := a.sessions[cookie.Value]
		a.mu.RUnlock()
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

func (a *API) dashboard(w http.ResponseWriter, r *http.Request) {
	const q = `
SELECT
    COUNT(*)::int AS total,
    COUNT(*) FILTER (WHERE device_status = 'available' AND session_status IS NULL)::int AS free,
    COUNT(*) FILTER (WHERE session_status = 'active')::int AS busy,
    COUNT(*) FILTER (WHERE session_status = 'reserved')::int AS reserved,
    COUNT(*) FILTER (WHERE session_status = 'paused')::int AS paused,
    COUNT(*) FILTER (WHERE device_status = 'maintenance')::int AS maintenance,
    COUNT(*) FILTER (WHERE device_status = 'offline')::int AS offline,
    COUNT(*) FILTER (
        WHERE session_status = 'active'
          AND planned_end_at IS NOT NULL
          AND planned_end_at <= NOW() + INTERVAL '15 minutes'
          AND planned_end_at > NOW()
    )::int AS expiring
FROM (
    SELECT
        d.status AS device_status,
        s.status AS session_status,
        s.planned_end_at
    FROM devices d
    LEFT JOIN sessions s
      ON s.device_id = d.id
     AND s.status IN ('reserved', 'active', 'paused')
) x`

	var result struct {
		Total       int `json:"total"`
		Free        int `json:"free"`
		Busy        int `json:"busy"`
		Reserved    int `json:"reserved"`
		Paused      int `json:"paused"`
		Maintenance int `json:"maintenance"`
		Offline     int `json:"offline"`
		Expiring    int `json:"expiring"`
	}

	if err := a.pool.QueryRow(r.Context(), q).Scan(
		&result.Total,
		&result.Free,
		&result.Busy,
		&result.Reserved,
		&result.Paused,
		&result.Maintenance,
		&result.Offline,
		&result.Expiring,
	); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

type Hall struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (a *API) listHalls(w http.ResponseWriter, r *http.Request) {
	rows, err := a.pool.Query(r.Context(), `
SELECT id, name, COALESCE(description, '')
FROM halls
ORDER BY id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	result := make([]Hall, 0)
	for rows.Next() {
		var h Hall
		if err := rows.Scan(&h.ID, &h.Name, &h.Description); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		result = append(result, h)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

type hallRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (a *API) createHall(w http.ResponseWriter, r *http.Request) {
	var req hallRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	var h Hall
	err := a.pool.QueryRow(r.Context(), `
INSERT INTO halls (name, description)
VALUES ($1, NULLIF($2, ''))
RETURNING id, name, COALESCE(description, '')`, req.Name, strings.TrimSpace(req.Description)).Scan(
		&h.ID, &h.Name, &h.Description,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "hall already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, h)
}

func (a *API) updateHall(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req hallRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	var h Hall
	err = a.pool.QueryRow(r.Context(), `
UPDATE halls
SET name = $2,
    description = NULLIF($3, ''),
    updated_at = NOW()
WHERE id = $1
RETURNING id, name, COALESCE(description, '')`, id, req.Name, strings.TrimSpace(req.Description)).Scan(
		&h.ID, &h.Name, &h.Description,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "hall not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, h)
}

func (a *API) deleteHall(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	_, err = a.pool.Exec(r.Context(), `DELETE FROM halls WHERE id = $1`, id)
	if err != nil {
		if strings.Contains(err.Error(), "foreign key") {
			writeError(w, http.StatusConflict, "hall contains devices")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type Client struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Phone    string `json:"phone"`
}

func (a *API) listClients(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("search"))

	rows, err := a.pool.Query(r.Context(), `
SELECT id, full_name, phone
FROM clients
WHERE $1 = ''
   OR full_name ILIKE '%' || $1 || '%'
   OR phone ILIKE '%' || $1 || '%'
ORDER BY id DESC`, search)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	result := make([]Client, 0)
	for rows.Next() {
		var c Client
		if err := rows.Scan(&c.ID, &c.FullName, &c.Phone); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

type clientRequest struct {
	FullName string `json:"full_name"`
	Phone    string `json:"phone"`
}

func (a *API) createClient(w http.ResponseWriter, r *http.Request) {
	var req clientRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.FullName = strings.TrimSpace(req.FullName)
	req.Phone = strings.TrimSpace(req.Phone)
	if req.FullName == "" || req.Phone == "" {
		writeError(w, http.StatusBadRequest, "full_name and phone are required")
		return
	}

	var c Client
	err := a.pool.QueryRow(r.Context(), `
INSERT INTO clients (full_name, phone)
VALUES ($1, $2)
RETURNING id, full_name, phone`, req.FullName, req.Phone).Scan(
		&c.ID, &c.FullName, &c.Phone,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "phone already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, c)
}

func (a *API) updateClient(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req clientRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.FullName = strings.TrimSpace(req.FullName)
	req.Phone = strings.TrimSpace(req.Phone)
	if req.FullName == "" || req.Phone == "" {
		writeError(w, http.StatusBadRequest, "full_name and phone are required")
		return
	}

	var c Client
	err = a.pool.QueryRow(r.Context(), `
UPDATE clients
SET full_name = $2,
    phone = $3,
    updated_at = NOW()
WHERE id = $1
RETURNING id, full_name, phone`, id, req.FullName, req.Phone).Scan(
		&c.ID, &c.FullName, &c.Phone,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "client not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, c)
}

func (a *API) deleteClient(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	_, err = a.pool.Exec(r.Context(), `DELETE FROM clients WHERE id = $1`, id)
	if err != nil {
		if strings.Contains(err.Error(), "foreign key") {
			writeError(w, http.StatusConflict, "client has sessions")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type Device struct {
	ID           int64      `json:"id"`
	HallID       int64      `json:"hall_id"`
	HallName     string     `json:"hall_name"`
	Type         string     `json:"type"`
	Number       int        `json:"number"`
	Name         string     `json:"name"`
	BaseStatus   string     `json:"base_status"`
	Status       string     `json:"status"`
	SessionID    *int64     `json:"session_id,omitempty"`
	ClientID     *int64     `json:"client_id,omitempty"`
	ClientName   *string    `json:"client_name,omitempty"`
	PlannedEndAt *time.Time `json:"planned_end_at,omitempty"`
}

const deviceStatusExpr = `
CASE
    WHEN d.status = 'maintenance' THEN 'maintenance'
    WHEN d.status = 'offline' THEN 'offline'
    WHEN s.status = 'reserved' THEN 'reserved'
    WHEN s.status = 'paused' THEN 'paused'
    WHEN s.status = 'active'
         AND s.planned_end_at IS NOT NULL
         AND s.planned_end_at <= NOW() + INTERVAL '15 minutes'
         AND s.planned_end_at > NOW() THEN 'expiring'
    WHEN s.status = 'active' THEN 'busy'
    ELSE 'free'
END`

func (a *API) listDevices(w http.ResponseWriter, r *http.Request) {
	query := `
SELECT
    d.id,
    d.hall_id,
    h.name,
    d.type,
    d.number,
    COALESCE(d.name, ''),
    d.status,
    ` + deviceStatusExpr + `,
    s.id,
    s.client_id,
    c.full_name,
    s.planned_end_at
FROM devices d
JOIN halls h ON h.id = d.hall_id
LEFT JOIN sessions s
  ON s.device_id = d.id
 AND s.status IN ('reserved', 'active', 'paused')
LEFT JOIN clients c ON c.id = s.client_id
`

	args := make([]any, 0, 1)
	where := make([]string, 0, 2)
	if hallID := strings.TrimSpace(r.URL.Query().Get("hall_id")); hallID != "" {
		id, err := strconv.ParseInt(hallID, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid hall_id")
			return
		}
		args = append(args, id)
		where = append(where, "d.hall_id = $1")
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY d.hall_id, d.number"

	rows, err := a.pool.Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	result := make([]Device, 0)
	for rows.Next() {
		var d Device
		if err := rows.Scan(
			&d.ID,
			&d.HallID,
			&d.HallName,
			&d.Type,
			&d.Number,
			&d.Name,
			&d.BaseStatus,
			&d.Status,
			&d.SessionID,
			&d.ClientID,
			&d.ClientName,
			&d.PlannedEndAt,
		); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

type deviceRequest struct {
	HallID int64  `json:"hall_id"`
	Type   string `json:"type"`
	Number int    `json:"number"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (a *API) createDevice(w http.ResponseWriter, r *http.Request) {
	var req deviceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.HallID <= 0 || req.Number <= 0 {
		writeError(w, http.StatusBadRequest, "hall_id and positive number are required")
		return
	}
	if req.Type != "pc" && req.Type != "console" {
		writeError(w, http.StatusBadRequest, "type must be pc or console")
		return
	}
	if req.Status == "" {
		req.Status = "available"
	}
	if req.Status != "available" && req.Status != "maintenance" && req.Status != "offline" {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}

	var id int64
	err := a.pool.QueryRow(r.Context(), `
INSERT INTO devices (hall_id, type, number, name, status)
VALUES ($1, $2, $3, NULLIF($4, ''), $5)
RETURNING id`, req.HallID, req.Type, req.Number, strings.TrimSpace(req.Name), req.Status).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "device number already exists in this hall")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (a *API) updateDevice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req deviceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.HallID <= 0 || req.Number <= 0 {
		writeError(w, http.StatusBadRequest, "hall_id and positive number are required")
		return
	}
	if req.Type != "pc" && req.Type != "console" {
		writeError(w, http.StatusBadRequest, "type must be pc or console")
		return
	}
	if req.Status != "available" && req.Status != "maintenance" && req.Status != "offline" {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}

	command, err := a.pool.Exec(r.Context(), `
UPDATE devices
SET hall_id = $2,
    type = $3,
    number = $4,
    name = NULLIF($5, ''),
    status = $6,
    updated_at = NOW()
WHERE id = $1`, id, req.HallID, req.Type, req.Number, strings.TrimSpace(req.Name), req.Status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	_, err = a.pool.Exec(r.Context(), `DELETE FROM devices WHERE id = $1`, id)
	if err != nil {
		if strings.Contains(err.Error(), "foreign key") {
			writeError(w, http.StatusConflict, "device has sessions")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type Session struct {
	ID                    int64      `json:"id"`
	ClientID              int64      `json:"client_id"`
	ClientName            string     `json:"client_name"`
	DeviceID              int64      `json:"device_id"`
	HallName              string     `json:"hall_name"`
	DeviceNumber          int        `json:"device_number"`
	DeviceType            string     `json:"device_type"`
	Status                string     `json:"status"`
	StartedAt             *time.Time `json:"started_at,omitempty"`
	PlannedEndAt          *time.Time `json:"planned_end_at,omitempty"`
	PausedAt              *time.Time `json:"paused_at,omitempty"`
	EndedAt               *time.Time `json:"ended_at,omitempty"`
	DurationMinutes       int        `json:"duration_minutes"`
	ActualDurationMinutes *int       `json:"actual_duration_minutes,omitempty"`
}

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))

	rows, err := a.pool.Query(r.Context(), `
SELECT
    s.id,
    s.client_id,
    c.full_name,
    s.device_id,
    h.name,
    d.number,
    d.type,
    s.status,
    s.started_at,
    s.planned_end_at,
    s.paused_at,
    s.ended_at,
    s.duration_minutes,
    s.actual_duration_minutes
FROM sessions s
JOIN clients c ON c.id = s.client_id
JOIN devices d ON d.id = s.device_id
JOIN halls h ON h.id = d.hall_id
WHERE $1 = '' OR s.status = $1
ORDER BY CASE WHEN s.status IN ('reserved', 'active', 'paused') THEN 0 ELSE 1 END, s.id DESC`, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	result := make([]Session, 0)
	for rows.Next() {
		var s Session
		if err := rows.Scan(
			&s.ID,
			&s.ClientID,
			&s.ClientName,
			&s.DeviceID,
			&s.HallName,
			&s.DeviceNumber,
			&s.DeviceType,
			&s.Status,
			&s.StartedAt,
			&s.PlannedEndAt,
			&s.PausedAt,
			&s.EndedAt,
			&s.DurationMinutes,
			&s.ActualDurationMinutes,
		); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

type sessionRequest struct {
	ClientID int64 `json:"client_id"`
	DeviceID int64 `json:"device_id"`
	Minutes  int   `json:"minutes"`
	Reserved bool  `json:"reserved"`
}

func (a *API) createSession(w http.ResponseWriter, r *http.Request) {
	var req sessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ClientID <= 0 || req.DeviceID <= 0 || req.Minutes <= 0 {
		writeError(w, http.StatusBadRequest, "client_id, device_id and positive minutes are required")
		return
	}

	var baseStatus string
	var current int
	err := a.pool.QueryRow(r.Context(), `
SELECT
    d.status,
    COUNT(s.id)::int
FROM devices d
LEFT JOIN sessions s
  ON s.device_id = d.id
 AND s.status IN ('reserved', 'active', 'paused')
WHERE d.id = $1
GROUP BY d.id, d.status`, req.DeviceID).Scan(&baseStatus, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if baseStatus != "available" {
		writeError(w, http.StatusConflict, "device is not available")
		return
	}
	if current > 0 {
		writeError(w, http.StatusConflict, "device already has an active session")
		return
	}

	status := "active"
	var started any = time.Now()
	var endAt any = time.Now().Add(time.Duration(req.Minutes) * time.Minute)
	if req.Reserved {
		status = "reserved"
		started = nil
		endAt = nil
	}

	var id int64
	err = a.pool.QueryRow(r.Context(), `
INSERT INTO sessions (client_id, device_id, status, started_at, planned_end_at, duration_minutes)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id`, req.ClientID, req.DeviceID, status, started, endAt, req.Minutes).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "device already has an active session")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (a *API) startSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	command, err := a.pool.Exec(r.Context(), `
UPDATE sessions
SET status = 'active',
    started_at = NOW(),
    planned_end_at = NOW() + (duration_minutes * INTERVAL '1 minute'),
    paused_at = NULL,
    paused_minutes = 0,
    actual_duration_minutes = NULL,
    updated_at = NOW()
WHERE id = $1
  AND status = 'reserved'`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "session is not reserved")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) pauseSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	command, err := a.pool.Exec(r.Context(), `
UPDATE sessions
SET status = 'paused',
    paused_at = NOW(),
    updated_at = NOW()
WHERE id = $1
  AND status = 'active'`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "session is not active")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) resumeSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	command, err := a.pool.Exec(r.Context(), `
UPDATE sessions
SET status = 'active',
    planned_end_at = planned_end_at + (NOW() - paused_at),
    paused_minutes = paused_minutes + GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (NOW() - paused_at)) / 60)::INTEGER),
    paused_at = NULL,
    updated_at = NOW()
WHERE id = $1
  AND status = 'paused'
  AND paused_at IS NOT NULL`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "session is not paused")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type extendRequest struct {
	Minutes int `json:"minutes"`
}

func (a *API) extendSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req extendRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Minutes <= 0 {
		writeError(w, http.StatusBadRequest, "minutes must be positive")
		return
	}

	command, err := a.pool.Exec(r.Context(), `
UPDATE sessions
SET planned_end_at = CASE
        WHEN status = 'reserved' THEN NULL
        ELSE planned_end_at + ($2 * INTERVAL '1 minute')
    END,
    duration_minutes = duration_minutes + $2,
    updated_at = NOW()
WHERE id = $1
  AND status IN ('reserved', 'active', 'paused')`, id, req.Minutes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "session is not active")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type transferRequest struct {
	DeviceID int64 `json:"device_id"`
}

func (a *API) transferSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req transferRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DeviceID <= 0 {
		writeError(w, http.StatusBadRequest, "device_id is required")
		return
	}

	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(r.Context())

	var sourceDeviceID int64
	var sessionStatus string
	err = tx.QueryRow(r.Context(), `
SELECT device_id, status
FROM sessions
WHERE id = $1
FOR UPDATE`, id).Scan(&sourceDeviceID, &sessionStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessionStatus != "active" && sessionStatus != "paused" {
		writeError(w, http.StatusConflict, "session is not active")
		return
	}
	if sourceDeviceID == req.DeviceID {
		writeError(w, http.StatusBadRequest, "same device")
		return
	}

	var targetStatus string
	var targetSessions int
	err = tx.QueryRow(r.Context(), `
SELECT
    d.status,
    COUNT(s.id)::int
FROM devices d
LEFT JOIN sessions s
  ON s.device_id = d.id
 AND s.status IN ('reserved', 'active', 'paused')
WHERE d.id = $1
GROUP BY d.id, d.status`, req.DeviceID).Scan(&targetStatus, &targetSessions)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "target device not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if targetStatus != "available" || targetSessions > 0 {
		writeError(w, http.StatusConflict, "target device is not available")
		return
	}

	if _, err := tx.Exec(r.Context(), `
UPDATE sessions
SET device_id = $2,
    updated_at = NOW()
WHERE id = $1`, id, req.DeviceID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) finishSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	command, err := a.pool.Exec(r.Context(), `
UPDATE sessions
SET status = 'finished',
    ended_at = NOW(),
    paused_minutes = paused_minutes + CASE
        WHEN paused_at IS NULL THEN 0
        ELSE GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (NOW() - paused_at)) / 60)::INTEGER)
    END,
    paused_at = NULL,
    actual_duration_minutes = CASE
        WHEN started_at IS NULL THEN 0
        ELSE GREATEST(
            0,
            FLOOR(
                EXTRACT(EPOCH FROM (NOW() - started_at)) / 60
                - paused_minutes
                - CASE
                    WHEN paused_at IS NULL THEN 0
                    ELSE EXTRACT(EPOCH FROM (NOW() - paused_at)) / 60
                  END
            )::INTEGER
        )
    END,
    updated_at = NOW()
WHERE id = $1
  AND status IN ('reserved', 'active', 'paused')`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "session is already finished")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
