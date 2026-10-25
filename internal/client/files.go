package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/zjywill/khoomei/internal/protocol"
)

// RequireBlobs fails early, before anything is uploaded, when the hub predates file transfer.
func (c *Client) RequireBlobs(ctx context.Context) error {
	h, err := c.Health(ctx)
	if err != nil {
		return err
	}
	if !h.HasFeature("blob") {
		return fmt.Errorf("the hub (%s) does not support files yet; upgrade it to 0.5 or later", h.Version)
	}
	return nil
}

// UploadFile puts a local file on the hub as agent unless agent may already use it there, and returns
// the reference to attach to a message.
func (c *Client) UploadFile(ctx context.Context, agent, path string) (protocol.FileRef, error) {
	f, err := os.Open(path)
	if err != nil {
		return protocol.FileRef{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return protocol.FileRef{}, err
	}
	if !st.Mode().IsRegular() {
		return protocol.FileRef{}, fmt.Errorf("%s is not a regular file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return protocol.FileRef{}, err
	}
	ref := protocol.FileRef{SHA256: hex.EncodeToString(h.Sum(nil)), Name: SafeName(filepath.Base(path)), Size: st.Size(),
		Type: mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))}
	if ok, err := c.HasBlob(ctx, agent, ref.SHA256); err != nil {
		return ref, err
	} else if ok {
		return ref, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ref, err
	}
	resp, err := c.PutBlob(ctx, agent, f, ref.Size, ref.SHA256, ref.Type)
	if err != nil {
		return ref, err
	}
	if !resp.OK {
		return ref, fmt.Errorf("upload %s: %s (%d)", path, resp.Error, resp.Status)
	}
	return ref, nil
}

// FetchFiles downloads a message's attachments into dir and returns the saved paths. Names are
// sanitized and never overwrite an existing file; content whose hash does not match is discarded.
func (c *Client) FetchFiles(ctx context.Context, agent string, files []protocol.FileRef, dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	saved := []string{}
	for _, f := range files {
		p, err := c.fetchOne(ctx, agent, f, dir)
		if err != nil {
			return saved, err
		}
		saved = append(saved, p)
	}
	return saved, nil
}

func (c *Client) fetchOne(ctx context.Context, agent string, f protocol.FileRef, dir string) (string, error) {
	if !protocol.ValidSHA256(f.SHA256) {
		return "", errors.New("bad sha256")
	}
	tmp, err := os.CreateTemp(dir, ".fetch-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	h := sha256.New()
	err = c.GetBlob(ctx, agent, f.SHA256, f.Name, f.Size, io.MultiWriter(tmp, h))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != f.SHA256 {
		return "", fmt.Errorf("%s: content hash %s does not match %s", f.Name, got[:12], f.SHA256[:12])
	}
	dest := uniquePath(dir, SafeName(f.Name))
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", err
	}
	return dest, nil
}

// SafeName turns a sender-chosen file name into one component that stays inside its directory.
func SafeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	name = strings.TrimLeft(name, ".")
	if r := []rune(name); len(r) > 200 {
		name = string(r[:200])
	}
	if name == "" {
		name = "file"
	}
	return name
}

// uniquePath appends " (2)", " (3)"… before the extension until the name is free.
func uniquePath(dir, name string) string {
	p := filepath.Join(dir, name)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
}
