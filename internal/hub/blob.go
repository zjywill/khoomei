package hub

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"time"

	"github.com/zjywill/khoomei/internal/blobs"
	"github.com/zjywill/khoomei/internal/disk"
	"github.com/zjywill/khoomei/internal/protocol"
)

// checkFiles: every attachment must already be on the hub with the stated size, and from_agent must
// have uploaded it or received it before, so a guessed hash cannot pull someone else's file into a
// message (docs/FILES.md).
func (h *Hub) checkFiles(env *protocol.Envelope) error {
	for _, f := range env.Files {
		meta, err := h.Blobs.Stat(f.SHA256)
		if errors.Is(err, os.ErrNotExist) || (err == nil && !meta.Has(env.FromAgent)) {
			return BadRequestError("unknown blob " + f.SHA256 + "; upload it with PUT /blob first")
		}
		if err != nil {
			return err
		}
		if meta.Size != f.Size {
			return BadRequestError(fmt.Sprintf("blob %s is %d bytes, not %d", f.SHA256, meta.Size, f.Size))
		}
	}
	return nil
}

func (h *Hub) handleBlobPut(w http.ResponseWriter, r *http.Request, id Identity) {
	agent := r.URL.Query().Get("agent")
	if !protocol.ValidAddress(agent) {
		fail(w, BadRequestError("bad agent"))
		return
	}
	agent = h.Qualify(agent)
	if err := h.Authorize(id, agent); err != nil {
		fail(w, err)
		return
	}
	max := h.Cfg.Blob.MaxBytes()
	switch {
	case r.ContentLength < 0:
		writeJSON(w, 411, map[string]any{"ok": false, "error": "Content-Length required"})
		return
	case r.ContentLength > max:
		writeJSON(w, 413, map[string]any{"ok": false, "error": fmt.Sprintf("file larger than %d MB", max>>20)})
		return
	}
	if free, total := disk.Space(h.Cfg.Home); total > 0 && free < h.Cfg.Blob.MinFreeBytes()+uint64(r.ContentLength) {
		writeJSON(w, 507, map[string]any{"ok": false, "error": "hub disk nearly full"})
		return
	}
	// The cap on all files together; content already on the hub costs nothing.
	if want := r.Header.Get("X-Khoomei-SHA256"); !protocol.ValidSHA256(want) || func() bool { _, err := h.Blobs.Stat(want); return err != nil }() {
		used, err := h.Blobs.Total()
		if err != nil {
			fail(w, err)
			return
		}
		if limit := h.Cfg.Blob.TotalBytes(); used+r.ContentLength > limit {
			writeJSON(w, 507, map[string]any{"ok": false, "error": fmt.Sprintf(
				"hub file storage full (%d of %d MB); files are freed once their messages are acked", used>>20, limit>>20)})
			return
		}
	}
	typ, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	sha, dup, err := h.Blobs.Put(r.Body, r.ContentLength, max, r.Header.Get("X-Khoomei-SHA256"), typ, agent)
	switch {
	case errors.Is(err, blobs.ErrTooLarge):
		writeJSON(w, 413, map[string]any{"ok": false, "error": err.Error()})
		return
	case errors.Is(err, blobs.ErrMismatch), errors.Is(err, blobs.ErrShort):
		fail(w, BadRequestError(err.Error()))
		return
	case err != nil:
		fail(w, err)
		return
	}
	code := 201
	if dup {
		code = 200
	}
	writeJSON(w, code, map[string]any{"ok": true, "sha256": sha, "size": r.ContentLength, "duplicate": dup})
}

// handleBlobGet serves a blob to an agent that is one of its parties. Anyone else gets 404, so the
// answer never reveals whether a hash exists.
func (h *Hub) handleBlobGet(w http.ResponseWriter, r *http.Request, id Identity) {
	sha, agent := r.PathValue("sha"), r.URL.Query().Get("agent")
	notFound := func() { writeJSON(w, 404, map[string]any{"ok": false, "error": "no such blob"}) }
	if !protocol.ValidSHA256(sha) || !protocol.ValidAddress(agent) {
		notFound()
		return
	}
	agent = h.Qualify(agent)
	if err := h.Authorize(id, agent); err != nil {
		fail(w, err)
		return
	}
	meta, err := h.Blobs.Stat(sha)
	if err != nil || !meta.Has(agent) {
		notFound()
		return
	}
	name := r.URL.Query().Get("name")
	if !protocol.ValidFileName(name) {
		name = sha
	}
	ServeBlob(w, r, h.Blobs.Path(sha), sha, name, "application/octet-stream", true)
}

// ServeBlob sends a stored file with Range and ETag support. Unless the caller has vetted the type,
// it goes out as an octet-stream attachment that the browser will neither sniff nor render.
func ServeBlob(w http.ResponseWriter, r *http.Request, path, sha, name, typ string, attachment bool) {
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "no such blob"})
		return
	}
	defer f.Close()
	hdr := w.Header()
	hdr.Set("Content-Type", typ)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("ETag", `"`+sha+`"`)
	hdr.Set("Cache-Control", "private, max-age=31536000, immutable")
	if attachment {
		hdr.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	http.ServeContent(w, r, "", time.Time{}, f)
}
