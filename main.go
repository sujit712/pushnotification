package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SherClockHolmes/webpush-go"
	_ "modernc.org/sqlite"
)

const (
	defaultPort        = "3000"
	defaultStorageMode = "memory"
	defaultJSONPath    = "./subscriptions.json"
	defaultSQLitePath  = "./subscriptions.db"
	maxJSONBodyBytes   = 1 << 20
	notificationTitle  = "Hello 👋"
	notificationBody   = "Hello World push from your Go VPS backend!"
	notificationURL    = "/"
)

type config struct {
	Port            string
	VAPIDSubject    string
	VAPIDPublicB64  string
	VAPIDPrivateKey string
	AdminToken      string
	StorageMode     string
	StoragePath     string
}

type app struct {
	cfg   config
	store SubscriptionStore
	debug *debugState
}

type subscribeRequest struct {
	DeviceID     string                  `json:"deviceId"`
	Subscription browserPushSubscription `json:"subscription"`
}

type browserPushSubscription struct {
	Endpoint       string                      `json:"endpoint"`
	ExpirationTime any                         `json:"expirationTime,omitempty"`
	Keys           browserPushSubscriptionKeys `json:"keys"`
}

type browserPushSubscriptionKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type sendHelloResponse struct {
	Sent    int `json:"sent"`
	Failed  int `json:"failed"`
	Removed int `json:"removed"`
}

type sendNotificationRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

type deviceSendResult struct {
	DeviceID     string `json:"deviceId"`
	EndpointHost string `json:"endpointHost"`
	StatusCode   int    `json:"statusCode"`
	Status       string `json:"status"`
	Sent         bool   `json:"sent"`
	Failed       bool   `json:"failed"`
	Removed      bool   `json:"removed"`
	Error        string `json:"error,omitempty"`
	Response     string `json:"response,omitempty"`
}

type debugState struct {
	mu         sync.RWMutex
	lastSendAt time.Time
	lastResult []deviceSendResult
}

type debugSubscription struct {
	DeviceID      string `json:"deviceId"`
	EndpointHost  string `json:"endpointHost"`
	EndpointShort string `json:"endpointShort"`
}

type debugResponse struct {
	SubscriptionCount int                 `json:"subscriptionCount"`
	Subscriptions     []debugSubscription `json:"subscriptions"`
	LastSendAt        string              `json:"lastSendAt,omitempty"`
	LastSendResult    []deviceSendResult  `json:"lastSendResult"`
}

type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

type storedSubscription struct {
	DeviceID     string               `json:"deviceId"`
	Subscription webpush.Subscription `json:"subscription"`
}

type SubscriptionStore interface {
	Upsert(ctx context.Context, deviceID string, sub webpush.Subscription) error
	List(ctx context.Context) ([]storedSubscription, error)
	Delete(ctx context.Context, deviceID string) error
}

type memoryStore struct {
	mu    sync.RWMutex
	items map[string]webpush.Subscription
}

func newMemoryStore() *memoryStore {
	return &memoryStore{items: make(map[string]webpush.Subscription)}
}

func (s *memoryStore) Upsert(_ context.Context, deviceID string, sub webpush.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[deviceID] = sub
	return nil
}

func (s *memoryStore) List(_ context.Context) ([]storedSubscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]storedSubscription, 0, len(s.items))
	for id, sub := range s.items {
		out = append(out, storedSubscription{DeviceID: id, Subscription: sub})
	}
	return out, nil
}

func (s *memoryStore) Delete(_ context.Context, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, deviceID)
	return nil
}

type jsonStore struct {
	mu    sync.RWMutex
	path  string
	items map[string]webpush.Subscription
}

func newJSONStore(path string) (*jsonStore, error) {
	s := &jsonStore{path: path, items: make(map[string]webpush.Subscription)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *jsonStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read storage file: %w", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	var list []storedSubscription
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("decode storage file: %w", err)
	}
	for _, item := range list {
		s.items[item.DeviceID] = item.Subscription
	}
	return nil
}

func (s *jsonStore) persistLocked() error {
	list := make([]storedSubscription, 0, len(s.items))
	for id, sub := range s.items {
		list = append(list, storedSubscription{DeviceID: id, Subscription: sub})
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("encode storage file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create storage directory: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write temp storage file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace storage file: %w", err)
	}
	return nil
}

func (s *jsonStore) Upsert(_ context.Context, deviceID string, sub webpush.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[deviceID] = sub
	return s.persistLocked()
}

func (s *jsonStore) List(_ context.Context) ([]storedSubscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]storedSubscription, 0, len(s.items))
	for id, sub := range s.items {
		out = append(out, storedSubscription{DeviceID: id, Subscription: sub})
	}
	return out, nil
}

func (s *jsonStore) Delete(_ context.Context, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, deviceID)
	return s.persistLocked()
}

type sqliteStore struct {
	db *sql.DB
}

func newSQLiteStore(path string) (*sqliteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := createSQLiteSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &sqliteStore{db: db}, nil
}

func createSQLiteSchema(db *sql.DB) error {
	const q = `
CREATE TABLE IF NOT EXISTS subscriptions (
	device_id TEXT PRIMARY KEY,
	subscription_json TEXT NOT NULL,
	updated_at TEXT NOT NULL
);`
	if _, err := db.Exec(q); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	return nil
}

func (s *sqliteStore) Upsert(ctx context.Context, deviceID string, sub webpush.Subscription) error {
	b, err := json.Marshal(sub)
	if err != nil {
		return fmt.Errorf("encode subscription: %w", err)
	}
	const q = `
INSERT INTO subscriptions (device_id, subscription_json, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(device_id) DO UPDATE SET
	subscription_json = excluded.subscription_json,
	updated_at = excluded.updated_at;`
	_, err = s.db.ExecContext(ctx, q, deviceID, string(b), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert subscription: %w", err)
	}
	return nil
}

func (s *sqliteStore) List(ctx context.Context) ([]storedSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, subscription_json FROM subscriptions`)
	if err != nil {
		return nil, fmt.Errorf("query subscriptions: %w", err)
	}
	defer rows.Close()

	var out []storedSubscription
	for rows.Next() {
		var deviceID, subJSON string
		if err := rows.Scan(&deviceID, &subJSON); err != nil {
			return nil, fmt.Errorf("scan subscription row: %w", err)
		}
		var sub webpush.Subscription
		if err := json.Unmarshal([]byte(subJSON), &sub); err != nil {
			return nil, fmt.Errorf("decode subscription row for device %q: %w", deviceID, err)
		}
		out = append(out, storedSubscription{DeviceID: deviceID, Subscription: sub})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return out, nil
}

func (s *sqliteStore) Delete(ctx context.Context, deviceID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM subscriptions WHERE device_id = ?`, deviceID)
	if err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	return nil
}

func (s *sqliteStore) Close() error {
	return s.db.Close()
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	store, closeFn, err := newStore(cfg.StorageMode, cfg.StoragePath)
	if err != nil {
		log.Fatalf("storage init error: %v", err)
	}
	defer closeFn()

	a := &app{cfg: cfg, store: store, debug: &debugState{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /vapidPublicKey", a.handleVAPIDPublicKey)
	mux.HandleFunc("POST /subscribe", a.handleSubscribe)
	mux.HandleFunc("POST /sendHello", a.handleSendHello)
	mux.HandleFunc("POST /sendNotification", a.handleSendNotification)
	mux.HandleFunc("GET /debug/subscriptions", a.handleDebugSubscriptions)
	mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join("public", "sw.js"))
	})
	mux.Handle("/", http.FileServer(http.Dir("./public")))

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           loggingMiddleware(sameOriginMiddleware(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("starting server on :%s (storage=%s)", cfg.Port, cfg.StorageMode)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

func loadConfig() (config, error) {
	cfg := config{
		Port:           envOrDefault("PORT", defaultPort),
		VAPIDSubject:   strings.TrimSpace(os.Getenv("VAPID_SUBJECT")),
		VAPIDPublicB64: strings.TrimSpace(os.Getenv("VAPID_PUBLIC_B64")),
		AdminToken:     strings.TrimSpace(os.Getenv("ADMIN_TOKEN")),
		StorageMode:    strings.ToLower(strings.TrimSpace(envOrDefault("STORAGE_MODE", defaultStorageMode))),
		StoragePath:    strings.TrimSpace(os.Getenv("STORAGE_PATH")),
	}
	pemPath := strings.TrimSpace(os.Getenv("VAPID_PRIVATE_PEM_PATH"))

	if cfg.AdminToken == "" {
		return config{}, errors.New("ADMIN_TOKEN is required")
	}
	if cfg.VAPIDSubject == "" {
		return config{}, errors.New("VAPID_SUBJECT is required")
	}
	if cfg.VAPIDPublicB64 == "" {
		return config{}, errors.New("VAPID_PUBLIC_B64 is required")
	}
	if pemPath == "" {
		return config{}, errors.New("VAPID_PRIVATE_PEM_PATH is required")
	}

	key, err := loadVAPIDPrivateKeyFromPEM(pemPath)
	if err != nil {
		return config{}, fmt.Errorf("load VAPID private key: %w", err)
	}
	cfg.VAPIDPrivateKey = key

	switch cfg.StorageMode {
	case "memory":
		cfg.StoragePath = ""
	case "json":
		if cfg.StoragePath == "" {
			cfg.StoragePath = defaultJSONPath
		}
	case "sqlite":
		if cfg.StoragePath == "" {
			cfg.StoragePath = defaultSQLitePath
		}
	default:
		return config{}, fmt.Errorf("unsupported STORAGE_MODE %q (use memory|json|sqlite)", cfg.StorageMode)
	}

	return cfg, nil
}

func newStore(mode, path string) (SubscriptionStore, func(), error) {
	switch mode {
	case "memory":
		return newMemoryStore(), func() {}, nil
	case "json":
		s, err := newJSONStore(path)
		if err != nil {
			return nil, nil, err
		}
		return s, func() {}, nil
	case "sqlite":
		s, err := newSQLiteStore(path)
		if err != nil {
			return nil, nil, err
		}
		return s, func() {
			if err := s.Close(); err != nil {
				log.Printf("error closing sqlite db: %v", err)
			}
		}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported storage mode: %s", mode)
	}
}

func (a *app) handleVAPIDPublicKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"key": a.cfg.VAPIDPublicB64})
}

func (a *app) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var req subscribeRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.DeviceID) == "" {
		writeJSONError(w, http.StatusBadRequest, "deviceId is required")
		return
	}
	if strings.TrimSpace(req.Subscription.Endpoint) == "" {
		writeJSONError(w, http.StatusBadRequest, "subscription.endpoint is required")
		return
	}
	if strings.TrimSpace(req.Subscription.Keys.P256dh) == "" || strings.TrimSpace(req.Subscription.Keys.Auth) == "" {
		writeJSONError(w, http.StatusBadRequest, "subscription.keys.p256dh and subscription.keys.auth are required")
		return
	}

	sub := webpush.Subscription{
		Endpoint: req.Subscription.Endpoint,
		Keys: webpush.Keys{
			P256dh: req.Subscription.Keys.P256dh,
			Auth:   req.Subscription.Keys.Auth,
		},
	}

	if err := a.store.Upsert(r.Context(), req.DeviceID, sub); err != nil {
		log.Printf("subscribe upsert failed for device %q: %v", req.DeviceID, err)
		writeJSONError(w, http.StatusInternalServerError, "failed to save subscription")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "subscribed", "deviceId": req.DeviceID})
}

func (a *app) handleSendHello(w http.ResponseWriter, r *http.Request) {
	if !a.hasValidAdminToken(r) {
		writeJSONError(w, http.StatusUnauthorized, "invalid admin token")
		return
	}

	var body struct{}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	subs, err := a.store.List(r.Context())
	if err != nil {
		log.Printf("list subscriptions failed: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to load subscriptions")
		return
	}
	if len(subs) == 0 {
		writeJSON(w, http.StatusOK, sendHelloResponse{Sent: 0, Failed: 0, Removed: 0})
		return
	}

	payload, _ := json.Marshal(pushPayload{
		Title: notificationTitle,
		Body:  notificationBody,
		URL:   notificationURL,
	})

	result := a.sendPayload(r.Context(), subs, payload)
	writeJSON(w, http.StatusOK, result)
}

func (a *app) handleSendNotification(w http.ResponseWriter, r *http.Request) {
	if !a.hasValidAdminToken(r) {
		writeJSONError(w, http.StatusUnauthorized, "invalid admin token")
		return
	}

	var req sendNotificationRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeJSONError(w, http.StatusBadRequest, "title is required")
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeJSONError(w, http.StatusBadRequest, "body is required")
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		req.URL = "/"
	}
	req.URL = normalizeNotificationURL(req.URL)

	subs, err := a.store.List(r.Context())
	if err != nil {
		log.Printf("list subscriptions failed: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to load subscriptions")
		return
	}
	if len(subs) == 0 {
		writeJSON(w, http.StatusOK, sendHelloResponse{Sent: 0, Failed: 0, Removed: 0})
		return
	}

	payload, _ := json.Marshal(pushPayload{
		Title: req.Title,
		Body:  req.Body,
		URL:   req.URL,
	})

	result := a.sendPayload(r.Context(), subs, payload)
	writeJSON(w, http.StatusOK, result)
}

func (a *app) sendPayload(ctx context.Context, subs []storedSubscription, payload []byte) sendHelloResponse {
	result := sendHelloResponse{}
	details := make([]deviceSendResult, 0, len(subs))
	for _, item := range subs {
		detail := deviceSendResult{
			DeviceID:     item.DeviceID,
			EndpointHost: endpointHost(item.Subscription.Endpoint),
		}

		resp, err := webpush.SendNotification(payload, &item.Subscription, &webpush.Options{
			Subscriber:      a.cfg.VAPIDSubject,
			VAPIDPublicKey:  a.cfg.VAPIDPublicB64,
			VAPIDPrivateKey: a.cfg.VAPIDPrivateKey,
			TTL:             60,
		})
		if err != nil {
			result.Failed++
			detail.Failed = true
			detail.Error = err.Error()
			log.Printf("push failed for device %q: %v", item.DeviceID, err)
			details = append(details, detail)
			continue
		}

		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		detail.StatusCode = resp.StatusCode
		detail.Status = resp.Status
		if readErr == nil {
			detail.Response = strings.TrimSpace(string(respBody))
		}

		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			result.Removed++
			detail.Removed = true
			if err := a.store.Delete(ctx, item.DeviceID); err != nil {
				log.Printf("failed removing invalid subscription %q: %v", item.DeviceID, err)
			}
			details = append(details, detail)
			continue
		}
		if resp.StatusCode == http.StatusForbidden &&
			detail.EndpointHost == "web.push.apple.com" &&
			strings.Contains(detail.Response, "BadJwtToken") {
			result.Removed++
			detail.Removed = true
			if err := a.store.Delete(ctx, item.DeviceID); err != nil {
				log.Printf("failed removing apple BadJwtToken subscription %q: %v", item.DeviceID, err)
			}
			details = append(details, detail)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			result.Failed++
			detail.Failed = true
			log.Printf("push non-success status for device %q: %s", item.DeviceID, resp.Status)
			details = append(details, detail)
			continue
		}
		result.Sent++
		detail.Sent = true
		details = append(details, detail)
	}

	a.setLastSendResult(details)
	return result
}

func (a *app) handleDebugSubscriptions(w http.ResponseWriter, r *http.Request) {
	if !a.hasValidAdminToken(r) {
		writeJSONError(w, http.StatusUnauthorized, "invalid admin token")
		return
	}

	subs, err := a.store.List(r.Context())
	if err != nil {
		log.Printf("list subscriptions failed for debug endpoint: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to load subscriptions")
		return
	}

	items := make([]debugSubscription, 0, len(subs))
	for _, s := range subs {
		items = append(items, debugSubscription{
			DeviceID:      s.DeviceID,
			EndpointHost:  endpointHost(s.Subscription.Endpoint),
			EndpointShort: shortenEndpoint(s.Subscription.Endpoint),
		})
	}

	lastSendAt, lastResults := a.getLastSendResult()
	resp := debugResponse{
		SubscriptionCount: len(items),
		Subscriptions:     items,
		LastSendResult:    lastResults,
	}
	if !lastSendAt.IsZero() {
		resp.LastSendAt = lastSendAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

func decodeJSONBody(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("request body is required")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain only one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write JSON response failed: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func (a *app) hasValidAdminToken(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("X-Admin-Token")) == a.cfg.AdminToken
}

func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Host
}

func shortenEndpoint(endpoint string) string {
	if len(endpoint) <= 96 {
		return endpoint
	}
	return endpoint[:96] + "..."
}

func normalizeNotificationURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "/"
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "javascript:") {
		return "/"
	}
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return value
	}
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	if strings.HasPrefix(value, "/") {
		return value
	}

	// Bare host/path values like "www.google.com" become absolute HTTPS URLs.
	candidate := "https://" + value
	u, err := url.Parse(candidate)
	if err == nil && strings.Contains(u.Host, ".") {
		return candidate
	}
	return "/" + strings.TrimLeft(value, "/")
}

func (a *app) setLastSendResult(results []deviceSendResult) {
	a.debug.mu.Lock()
	defer a.debug.mu.Unlock()
	copied := make([]deviceSendResult, len(results))
	copy(copied, results)
	a.debug.lastResult = copied
	a.debug.lastSendAt = time.Now().UTC()
}

func (a *app) getLastSendResult() (time.Time, []deviceSendResult) {
	a.debug.mu.RLock()
	defer a.debug.mu.RUnlock()
	copied := make([]deviceSendResult, len(a.debug.lastResult))
	copy(copied, a.debug.lastResult)
	return a.debug.lastSendAt, copied
}

func sameOriginMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				writeJSONError(w, http.StatusForbidden, "cross-origin requests are not allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func loadVAPIDPrivateKeyFromPEM(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return "", errors.New("no PEM block found")
	}

	var ecKey *ecdsa.PrivateKey
	ecKey, err = x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		pkcs8Key, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return "", fmt.Errorf("parse EC private key: %w", err)
		}
		k, ok := pkcs8Key.(*ecdsa.PrivateKey)
		if !ok {
			return "", errors.New("PEM key is not an ECDSA private key")
		}
		ecKey = k
	}

	if ecKey.Curve == nil || ecKey.Curve.Params().Name != "P-256" {
		return "", errors.New("VAPID private key must use P-256 curve")
	}

	d := ecKey.D.Bytes()
	padded := leftPad(d, 32)
	return base64.RawURLEncoding.EncodeToString(padded), nil
}

func leftPad(in []byte, size int) []byte {
	if len(in) >= size {
		return in
	}
	out := make([]byte, size)
	copy(out[size-len(in):], in)
	return out
}
