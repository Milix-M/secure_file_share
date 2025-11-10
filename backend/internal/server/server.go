package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/gorilla/websocket"

	"ephemeralpod/internal/config"
	"ephemeralpod/internal/storage"
	"ephemeralpod/internal/ws"
)

// Server wires HTTP handlers, the storage manager, and configuration together.
type Server struct {
	cfg        config.Config
	storage    *storage.Manager
	logger     *slog.Logger
	httpServer *http.Server
	upgrader   websocket.Upgrader
}

// New constructs a Server with routes registered.
func New(cfg config.Config, storage *storage.Manager, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	s := &Server{
		cfg:      cfg,
		storage:  storage,
		logger:   logger.WithGroup("http"),
		upgrader: ws.Upgrader(),
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(recoverer(logger))
	r.Use(securityHeaders())
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.AllowedOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token", "Origin"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", s.handleHealth)
	r.Post("/api/upload", s.handleUpload)
	r.Get("/download/{fileID}", s.handleDownloadMetadata)
	r.Get("/ws/connect/{fileID}", s.handleWebSocket)

	s.httpServer = &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		ReadHeaderTimeout: 5 * time.Second,
	}

	return s
}

// Run starts the HTTP server and blocks until the provided context is done or an unrecoverable error occurs.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		s.logger.Info("listening", "addr", s.cfg.Addr)
		err := s.httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		} else {
			close(errCh)
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// handleHealth returns a simple readiness signal.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		writeError(w, http.StatusUnsupportedMediaType, "content type must be multipart/form-data")
		return
	}

	limit := s.cfg.MaxUploadSize + 512*1024 // small cushion for multipart overhead
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()

	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart body")
		return
	}

	var (
		meta  storage.Metadata
		found bool
	)

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to parse multipart data")
			return
		}

		if part.FormName() != "file" {
			part.Close()
			continue
		}

		meta, err = s.storage.Save(r.Context(), part.FileName(), part)
		part.Close()
		if err != nil {
			s.handleStorageError(w, err)
			return
		}

		found = true
		break
	}

	if !found {
		writeError(w, http.StatusBadRequest, "file part not found")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"file_id":     meta.ID,
		"expires_at":  meta.ExpiresAt,
		"size":        meta.Size,
		"ttl_seconds": meta.OriginalTTL,
	})
}

func (s *Server) handleDownloadMetadata(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileID")
	if fileID == "" {
		writeError(w, http.StatusBadRequest, "missing file id")
		return
	}

	meta, err := s.storage.Peek(fileID)
	if err != nil {
		s.handleStorageError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"file_id":     meta.ID,
		"size":        meta.Size,
		"expires_at":  meta.ExpiresAt,
		"ttl_seconds": meta.OriginalTTL,
	})
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileID")
	if fileID == "" {
		writeError(w, http.StatusBadRequest, "missing file id")
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	ctx := r.Context()

	file, _, err := s.storage.Checkout(fileID)
	if err != nil {
		writeWebSocketError(conn, err)
		return
	}

	defer func() {
		file.Close()
		if removeErr := os.Remove(file.Name()); removeErr != nil {
			s.logger.Warn("failed to remove file")
		}
	}()

	if err := ws.Stream(ctx, conn, file, s.cfg.WebSocketIdlePing); err != nil {
		s.logger.Warn("stream failed")
		return
	}

	deadline := time.Now().Add(3 * time.Second)
	if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), deadline); err != nil {
		s.logger.Debug("failed to send ws close", "err", err)
	}
}

func (s *Server) handleStorageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrFileNotFound):
		writeError(w, http.StatusNotFound, "file not found")
	case errors.Is(err, storage.ErrFileExpired):
		writeError(w, http.StatusGone, "file expired")
	case errors.Is(err, storage.ErrSizeExceeded):
		writeError(w, http.StatusRequestEntityTooLarge, "file too large")
	case errors.Is(err, storage.ErrContextCanceled):
		writeError(w, http.StatusRequestTimeout, "upload canceled")
	case errors.Is(err, storage.ErrCapacityExceeded):
		writeError(w, http.StatusInsufficientStorage, "storage capacity exceeded")
	default:
		s.logger.Error("storage error")
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeWebSocketError(conn *websocket.Conn, err error) {
	defer conn.Close()

	code := websocket.CloseInternalServerErr
	msg := "internal error"

	switch {
	case errors.Is(err, storage.ErrFileNotFound):
		code = websocket.CloseNormalClosure
		msg = "file not found"
	case errors.Is(err, storage.ErrFileExpired):
		code = websocket.CloseNormalClosure
		msg = "file expired"
	}

	deadline := time.Now().Add(3 * time.Second)
	conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, msg), deadline)
}

func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered", "panic", rec)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func securityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			headers := w.Header()
			headers.Set("X-Content-Type-Options", "nosniff")
			headers.Set("Referrer-Policy", "no-referrer")
			headers.Set("X-Frame-Options", "DENY")
			headers.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
			headers.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
			next.ServeHTTP(w, r)
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		fmt.Fprintf(w, "{\"error\":\"%s\"}", http.StatusText(http.StatusInternalServerError))
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
