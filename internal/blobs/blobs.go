// Package blobs stores message attachments on the hub by content (docs/FILES.md):
// blobs/<aa>/<sha256>.bin holds the bytes, blobs/<aa>/<sha256>.json who may see them.
package blobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zjywill/khoomei/internal/protocol"
)

var (
	ErrTooLarge = errors.New("file too large")
	ErrMismatch = errors.New("content does not match X-Khoomei-SHA256")
	ErrShort    = errors.New("body shorter than Content-Length")
)

// Meta is the sidecar of a blob. Parties are the agents that may fetch it: whoever uploaded it, and
// both ends of every message that attached it.
type Meta struct {
	Size      int64    `json:"size"`
	Type      string   `json:"type,omitempty"` // Content-Type of the first upload; a hint only
	Uploaders []string `json:"uploaders"`
	Parties   []string `json:"parties"`
	FirstSeen string   `json:"first_seen"`
}

func (m *Meta) Has(agent string) bool { return slices.Contains(m.Parties, agent) }

// Seen is when the blob first reached the hub.
func (m *Meta) Seen() time.Time {
	t, _ := time.Parse(time.RFC3339, m.FirstSeen)
	return t
}

type Store struct {
	dir   string
	mu    sync.Mutex
	total int64 // bytes of all .bin files; valid once known is set
	known bool
}

func New(home string) *Store { return &Store{dir: filepath.Join(home, "blobs")} }

func (s *Store) path(sha, ext string) string { return filepath.Join(s.dir, sha[:2], sha+ext) }

// Path is where a blob's bytes live; callers must have checked the sha256 with protocol.ValidSHA256.
func (s *Store) Path(sha string) string { return s.path(sha, ".bin") }

// Put streams size bytes from r into the store as uploader. A non-empty want must equal the content's
// SHA-256. dup is true when the content was already there; uploader is added to it either way.
func (s *Store) Put(r io.Reader, size, max int64, want, typ, uploader string) (sha string, dup bool, err error) {
	if size > max {
		return "", false, ErrTooLarge
	}
	tmpDir := filepath.Join(s.dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", false, err
	}
	tmp, err := os.CreateTemp(tmpDir, "up-*")
	if err != nil {
		return "", false, err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, size+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return "", false, err
	case n > size:
		return "", false, fmt.Errorf("body longer than Content-Length")
	case n < size:
		return "", false, ErrShort
	}
	sha = hex.EncodeToString(h.Sum(nil))
	if want != "" && !strings.EqualFold(want, sha) {
		return "", false, ErrMismatch
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.read(sha)
	switch {
	case err == nil:
		dup = true
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(s.Path(sha)), 0o700); err != nil {
			return "", false, err
		}
		if err := os.Rename(tmp.Name(), s.Path(sha)); err != nil {
			return "", false, err
		}
		meta = &Meta{Size: n, Type: typ, FirstSeen: protocol.Now()}
		s.total += n
	default:
		return "", false, err
	}
	meta.Uploaders = addOnce(meta.Uploaders, uploader)
	meta.Parties = addOnce(meta.Parties, uploader)
	return sha, dup, s.write(sha, meta)
}

// Stat returns a blob's sidecar, or os.ErrNotExist.
func (s *Store) Stat(sha string) (*Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(sha)
}

// Attach records that a message from one agent to another carries these blobs, so both may fetch them.
func (s *Store) Attach(files []protocol.FileRef, from, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range files {
		meta, err := s.read(f.SHA256)
		if err != nil {
			return err
		}
		meta.Parties = addOnce(addOnce(meta.Parties, from), to)
		if err := s.write(f.SHA256, meta); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes a blob and its sidecar, returning the bytes freed.
func (s *Store) Remove(sha string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := os.Stat(s.Path(sha))
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(s.path(sha, ".json"))
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := os.Remove(s.Path(sha)); err != nil {
		return 0, err
	}
	s.total -= st.Size()
	_ = os.Remove(s.path(sha, ".json"))
	_ = os.Remove(filepath.Dir(s.Path(sha))) // only succeeds once the shard is empty
	return st.Size(), nil
}

// Total is the bytes all stored files take, counted from disk once and kept up to date after that.
func (s *Store) Total() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.known {
		return s.total, nil
	}
	var total int64
	err := filepath.WalkDir(s.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".bin") {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.total, s.known = total, true
	return total, nil
}

// Sweep removes what crashes leave behind, if older than cutoff: half-received uploads in tmp/, and
// contents whose sidecar was never written. It returns the files removed and bytes freed.
func (s *Store) Sweep(cutoff time.Time) (n int, freed int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = filepath.WalkDir(s.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			return nil
		}
		stray := filepath.Base(filepath.Dir(p)) == "tmp"
		if sha, ok := strings.CutSuffix(filepath.Base(p), ".bin"); ok && !stray {
			_, err := os.Stat(s.path(sha, ".json"))
			if stray = errors.Is(err, os.ErrNotExist); stray {
				s.total -= info.Size()
			}
		}
		if stray && os.Remove(p) == nil {
			n++
			freed += info.Size()
		}
		return nil
	})
	return n, freed
}

// Each calls fn for every stored blob; unreadable sidecars are skipped.
func (s *Store) Each(fn func(sha string, m Meta)) error {
	shards, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, d := range shards {
		if !d.IsDir() || len(d.Name()) != 2 {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(s.dir, d.Name()))
		for _, f := range files {
			sha, ok := strings.CutSuffix(f.Name(), ".json")
			if !ok || !protocol.ValidSHA256(sha) {
				continue
			}
			if m, err := s.Stat(sha); err == nil {
				fn(sha, *m)
			}
		}
	}
	return nil
}

// read loads a sidecar; the caller holds s.mu.
func (s *Store) read(sha string) (*Meta, error) {
	b, err := os.ReadFile(s.path(sha, ".json"))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("blob %s: %w", sha, err)
	}
	return &m, nil
}

// write saves a sidecar atomically; the caller holds s.mu.
func (s *Store) write(sha string, m *Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	p := s.path(sha, ".json")
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

func addOnce(list []string, v string) []string {
	if v == "" || slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}
