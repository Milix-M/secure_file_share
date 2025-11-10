package storage

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrFileNotFound     = errors.New("storage: file not found")
	ErrFileExpired      = errors.New("storage: file expired")
	ErrSizeExceeded     = errors.New("storage: file size exceeds limit")
	ErrContextCanceled  = errors.New("storage: context canceled")
	ErrCapacityExceeded = errors.New("storage: capacity exceeded")
)

// Metadata holds non-sensitive descriptive information about an uploaded blob.
type Metadata struct {
	ID          string    `json:"file_id"`
	Size        int64     `json:"size"`
	UploadedAt  time.Time `json:"uploaded_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	OriginalTTL int64     `json:"ttl_seconds"`
}

type fileRecord struct {
	Metadata
	path string
}

// Manager provides concurrency-safe, in-memory bookkeeping for files placed on a volatile filesystem.
type Manager struct {
	basePath  string
	maxSize   int64
	capacity  int64
	ttl       time.Duration
	mu        sync.RWMutex
	records   map[string]fileRecord
	current   int64
	cleanupCh chan struct{}
}

// NewManager constructs a Manager and ensures the target basePath exists.
func NewManager(basePath string, maxSize int64, ttl time.Duration, capacity int64) (*Manager, error) {
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		return nil, fmt.Errorf("ensure storage path: %w", err)
	}

	if err := purgeExisting(basePath); err != nil {
		return nil, fmt.Errorf("purge storage path: %w", err)
	}

	return &Manager{
		basePath:  basePath,
		maxSize:   maxSize,
		capacity:  capacity,
		ttl:       ttl,
		records:   make(map[string]fileRecord),
		cleanupCh: make(chan struct{}),
	}, nil
}

// Close signals the background routines to stop.
func (m *Manager) Close() {
	close(m.cleanupCh)
}

// StartCleanup launches a goroutine that purges expired uploads at the configured interval.
func (m *Manager) StartCleanup(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.cleanupCh:
				return
			case <-ticker.C:
				m.removeExpired()
			}
		}
	}()
}

// Save copies the provided data into the volatile storage and tracks its metadata.
func (m *Manager) Save(ctx context.Context, filename string, r io.Reader) (Metadata, error) {
	// Filename deliberately unused to avoid persisting potentially sensitive metadata.
	_ = filename
	id, err := generateID()
	if err != nil {
		return Metadata{}, err
	}

	path := filepath.Join(m.basePath, id)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Metadata{}, fmt.Errorf("create file: %w", err)
	}
	defer file.Close()

	limited := io.LimitReader(r, m.maxSize+1)
	size, err := io.Copy(file, limited)
	if err != nil {
		return Metadata{}, fmt.Errorf("write file: %w", err)
	}

	if size > m.maxSize {
		os.Remove(path)
		return Metadata{}, ErrSizeExceeded
	}

	select {
	case <-ctx.Done():
		os.Remove(path)
		return Metadata{}, ErrContextCanceled
	default:
	}

	uploadedAt := time.Now().UTC()
	expiresAt := uploadedAt.Add(m.ttl)

	meta := Metadata{
		ID:          id,
		Size:        size,
		UploadedAt:  uploadedAt,
		ExpiresAt:   expiresAt,
		OriginalTTL: int64(m.ttl.Seconds()),
	}

	m.mu.Lock()
	if err := m.ensureCapacityLocked(meta.Size); err != nil {
		m.mu.Unlock()
		os.Remove(path)
		return Metadata{}, err
	}
	m.records[id] = fileRecord{Metadata: meta, path: path}
	m.current += meta.Size
	m.mu.Unlock()

	return meta, nil
}

// Peek returns metadata without altering the lifecycle state.
func (m *Manager) Peek(id string) (Metadata, error) {
	m.mu.RLock()
	record, ok := m.records[id]
	m.mu.RUnlock()

	if !ok {
		return Metadata{}, ErrFileNotFound
	}

	if record.ExpiresAt.Before(time.Now().UTC()) {
		m.Delete(id)
		return Metadata{}, ErrFileExpired
	}

	return record.Metadata, nil
}

// Checkout removes the record from the manager and returns an open file ready for streaming.
func (m *Manager) Checkout(id string) (*os.File, Metadata, error) {
	m.mu.Lock()
	record, ok := m.records[id]
	if !ok {
		m.mu.Unlock()
		return nil, Metadata{}, ErrFileNotFound
	}

	if record.ExpiresAt.Before(time.Now().UTC()) {
		delete(m.records, id)
		m.current -= record.Size
		m.mu.Unlock()
		os.Remove(record.path)
		return nil, Metadata{}, ErrFileExpired
	}

	delete(m.records, id)
	m.current -= record.Size
	m.mu.Unlock()

	file, err := os.Open(record.path)
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("open file: %w", err)
	}

	return file, record.Metadata, nil
}

// Delete removes the file and metadata if present.
func (m *Manager) Delete(id string) {
	m.mu.Lock()
	record, ok := m.records[id]
	if ok {
		delete(m.records, id)
		m.current -= record.Size
	}
	m.mu.Unlock()

	if ok {
		os.Remove(record.path)
	}
}

func (m *Manager) removeExpired() {
	now := time.Now().UTC()

	var stale []fileRecord

	m.mu.Lock()
	for id, record := range m.records {
		if record.ExpiresAt.Before(now) {
			stale = append(stale, record)
			delete(m.records, id)
			m.current -= record.Size
		}
	}
	m.mu.Unlock()

	for _, record := range stale {
		os.Remove(record.path)
	}
}

func purgeExisting(basePath string) error {
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(basePath, entry.Name())); err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) ensureCapacityLocked(needed int64) error {
	if m.capacity <= 0 {
		return nil
	}
	if needed > m.capacity {
		return ErrCapacityExceeded
	}

	for m.current+needed > m.capacity {
		if !m.evictOldestLocked() {
			return ErrCapacityExceeded
		}
	}

	return nil
}

func (m *Manager) evictOldestLocked() bool {
	var (
		oldestID string
		oldest   fileRecord
	)

	for id, record := range m.records {
		if oldestID == "" || record.UploadedAt.Before(oldest.UploadedAt) {
			oldestID = id
			oldest = record
		}
	}

	if oldestID == "" {
		return false
	}

	delete(m.records, oldestID)
	m.current -= oldest.Size
	if err := os.Remove(oldest.path); err != nil {
		// roll back bookkeeping so callers can decide how to proceed
		m.records[oldestID] = oldest
		m.current += oldest.Size
		return false
	}

	return true
}

func generateID() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
