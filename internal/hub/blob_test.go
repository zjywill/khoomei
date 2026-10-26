package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zjywill/khoomei/internal/client"
	"github.com/zjywill/khoomei/internal/protocol"
)

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestBlobUploadAndAccess(t *testing.T) {
	h, srv := newTestHub(t)
	group(t, h, "proj", "junyi/grok", "alice/muse")
	ctx := context.Background()
	muse := asPeer(srv, museNode, "junyi@github")
	owner := client.New(srv.URL, nil) // loopback: may act as junyi/grok
	alice := asPeer(srv, aliceMuseNode, aliceLogin)
	body := []byte("a small report\n")
	sha := shaOf(body)

	resp, err := muse.PutBlob(ctx, "muse", bytes.NewReader(body), int64(len(body)), sha, "text/plain")
	if err != nil || resp.Status != 201 || resp.SHA256 != sha {
		t.Fatalf("put: %v %+v", err, resp)
	}
	if resp, _ = muse.PutBlob(ctx, "muse", bytes.NewReader(body), int64(len(body)), "", ""); resp.Status != 200 {
		t.Fatalf("duplicate put: %+v", resp)
	}
	if resp, _ = muse.PutBlob(ctx, "muse", bytes.NewReader(body), int64(len(body)), shaOf([]byte("x")), ""); resp.Status != 400 {
		t.Fatalf("mismatched hash accepted: %+v", resp)
	}
	if resp, _ = muse.PutBlob(ctx, "grok", bytes.NewReader(body), int64(len(body)), "", ""); resp.Status != 403 {
		t.Fatalf("upload as another agent: %+v", resp)
	}

	// Before any message, only the uploader can see it.
	if ok, _ := owner.HasBlob(ctx, "grok", sha); ok {
		t.Fatal("non-party sees the blob")
	}
	if ok, _ := muse.HasBlob(ctx, "muse", sha); !ok {
		t.Fatal("uploader cannot see its blob")
	}

	// Attaching makes the recipient a party.
	ref := protocol.FileRef{SHA256: sha, Name: "report.txt", Size: int64(len(body))}
	env := protocol.New("muse", "grok", "see attached")
	env.Files = []protocol.FileRef{ref}
	if r, err := muse.Send(ctx, env); err != nil || r.Status != 202 {
		t.Fatalf("send with file: %v %+v", err, r)
	}
	var got bytes.Buffer
	if err := owner.GetBlob(ctx, "grok", sha, "report.txt", ref.Size, &got); err != nil || got.String() != string(body) {
		t.Fatalf("recipient get: %v %q", err, got.String())
	}

	// A friend who never saw it cannot fetch it, nor attach it by hash.
	if ok, _ := alice.HasBlob(ctx, "alice/muse", sha); ok {
		t.Fatal("friend sees the blob")
	}
	steal := protocol.New("alice/muse", "grok", "")
	steal.Files = []protocol.FileRef{ref}
	if r, _ := alice.Send(ctx, steal); r.Status != 400 {
		t.Fatalf("attach by guessed hash: %+v", r)
	}

	// The recipient may forward what it received.
	fwd := protocol.New("grok", "alice/muse", "")
	fwd.Files = []protocol.FileRef{ref}
	if r, _ := owner.Send(ctx, fwd); r.Status != 202 {
		t.Fatalf("forward: %+v", r)
	}
	if ok, _ := alice.HasBlob(ctx, "alice/muse", sha); !ok {
		t.Fatal("forward recipient cannot fetch")
	}

	// Size must match what is stored.
	bad := protocol.New("grok", "muse", "")
	bad.Files = []protocol.FileRef{{SHA256: sha, Name: "x", Size: 1}}
	if r, _ := owner.Send(ctx, bad); r.Status != 400 {
		t.Fatalf("wrong size accepted: %+v", r)
	}
}

func TestBlobLimitsAndRange(t *testing.T) {
	h, srv := newTestHub(t)
	ctx := context.Background()
	owner := client.New(srv.URL, nil)
	h.Cfg.Blob.MaxMB = 1
	big := bytes.Repeat([]byte("k"), 1<<20+1)
	// The hub answers 413 without reading the body, so the client may see either the 413 or a broken pipe.
	if r, err := owner.PutBlob(ctx, "grok", bytes.NewReader(big), int64(len(big)), "", ""); err == nil && r.Status != 413 {
		t.Fatalf("over max: %+v", r)
	}
	if _, _, err := h.Blobs.Put(strings.NewReader("short"), 10, 1<<20, "", "", "junyi/grok"); err == nil {
		t.Fatal("short body accepted")
	}
	if entries, _ := os.ReadDir(filepath.Join(h.Cfg.Home, "blobs", "tmp")); len(entries) != 0 {
		t.Fatalf("temp files left behind: %v", entries)
	}

	body := []byte("0123456789")
	sha := shaOf(body)
	if r, _ := owner.PutBlob(ctx, "grok", bytes.NewReader(body), 10, "", ""); r.Status != 201 {
		t.Fatalf("put: %+v", r)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/blob/"+sha+"?agent=grok&name=../../etc/passwd", nil)
	req.Header.Set("Range", "bytes=3-5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(part) != "345" {
		t.Fatalf("range: %d %q", resp.StatusCode, part)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", resp.Header)
	}
	if cd := resp.Header.Get("Content-Disposition"); strings.Contains(cd, "passwd") {
		t.Fatalf("unsafe name echoed: %q", cd)
	}
	if code := getStatus(t, srv.URL+"/blob/"+strings.Repeat("0", 64)+"?agent=grok"); code != 404 {
		t.Fatalf("missing blob: %d", code)
	}
	if code := getStatus(t, srv.URL+"/blob/nothex?agent=grok"); code != 404 {
		t.Fatalf("bad hash: %d", code)
	}
}

func TestHealthFeatures(t *testing.T) {
	_, srv := newTestHub(t)
	c := client.New(srv.URL, nil)
	h, err := c.Health(context.Background())
	if err != nil || !h.HasFeature("blob") {
		t.Fatalf("health: %v %+v", err, h)
	}
	if err := c.RequireBlobs(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}
