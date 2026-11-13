package store

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

// Bundle is a set of file states and the content they need, as one tar
// stream: a .yonder-store.json entry first, then one entry per file.
type Bundle struct {
	V     int                  `json:"v"`
	Store string               `json:"store"`
	From  string               `json:"from"`
	Files map[string]FileState `json:"files"`
	Data  map[string][]byte    `json:"-"`
	// paths the receiver has, and should hold back from the sender (State.Held)
	Hold []string `json:"hold,omitempty"`
}

const bundleMeta = ".yonder-store.json"

// limit on a whole bundle stream, with room for tar headers
const maxBundle = MaxTotal + 16<<20

// WriteBundle writes b as a tar stream.
func WriteBundle(w io.Writer, b Bundle) error {
	b.V = ProtocolVersion
	meta, err := json.Marshal(b)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	now := time.Unix(0, 0)
	if err := tw.WriteHeader(&tar.Header{Name: bundleMeta, Mode: 0o600, Size: int64(len(meta)), ModTime: now, Format: tar.FormatPAX}); err != nil {
		return err
	}
	if _, err := tw.Write(meta); err != nil {
		return err
	}
	paths := make([]string, 0, len(b.Data))
	for p := range b.Data {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		data := b.Data[p]
		mode := int64(0o644)
		if b.Files[p].Exec {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: p, Mode: mode, Size: int64(len(data)), ModTime: now, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	return tw.Close()
}

// ReadBundle reads a whole bundle and checks every entry before anything
// uses it: names are clean store paths the metadata lists, files only,
// sizes within limits, content matching its hash.
func ReadBundle(r io.Reader) (Bundle, error) {
	tr := tar.NewReader(io.LimitReader(r, maxBundle))
	h, err := tr.Next()
	if err != nil {
		return Bundle{}, fmt.Errorf("reading bundle: %w", err)
	}
	if h.Name != bundleMeta || h.Size > 16<<20 {
		return Bundle{}, errors.New("bundle doesn't start with its metadata")
	}
	var b Bundle
	if err := json.NewDecoder(io.LimitReader(tr, h.Size)).Decode(&b); err != nil {
		return Bundle{}, fmt.Errorf("bundle metadata: %w", err)
	}
	if b.V < 1 || b.V > ProtocolVersion {
		return Bundle{}, fmt.Errorf("bundle version %d; this yonder speaks %d, so upgrade the older side", b.V, ProtocolVersion)
	}
	if b.Files == nil {
		b.Files = map[string]FileState{}
	}
	if len(b.Files) > 2*MaxFiles {
		return Bundle{}, fmt.Errorf("bundle lists %d files, over the limit", len(b.Files))
	}
	for p, f := range b.Files {
		if err := ValidRel(p); err != nil {
			return Bundle{}, err
		}
		f.Held = false // only this machine's own staleness logic sets it
		b.Files[p] = f
	}
	b.Data = map[string][]byte{}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Bundle{}, fmt.Errorf("reading bundle: %w", err)
		}
		f, ok := b.Files[h.Name]
		switch {
		case !ok || f.Deleted:
			return Bundle{}, fmt.Errorf("bundle entry %q isn't in its metadata", h.Name)
		case h.Typeflag != tar.TypeReg:
			return Bundle{}, fmt.Errorf("bundle entry %q is not a regular file", h.Name)
		case h.Size > MaxFile:
			return Bundle{}, fmt.Errorf("bundle entry %q is over %d MiB", h.Name, MaxFile>>20)
		case b.Data[h.Name] != nil:
			return Bundle{}, fmt.Errorf("bundle entry %q twice", h.Name)
		}
		total += h.Size
		if total > MaxTotal {
			return Bundle{}, fmt.Errorf("bundle is over %d MiB", MaxTotal>>20)
		}
		data, err := io.ReadAll(io.LimitReader(tr, h.Size))
		if err != nil {
			return Bundle{}, err
		}
		if int64(len(data)) != h.Size || hashOf(data) != f.Hash {
			return Bundle{}, fmt.Errorf("bundle entry %q doesn't match its hash", h.Name)
		}
		b.Data[h.Name] = data
	}
	return b, nil
}

// PushResult is what a receiver did with a bundle.
type PushResult struct {
	Applied []string  `json:"applied"`
	Pending []string  `json:"pending"`
	Skipped []Skip    `json:"skipped"`
	Refused []Refusal `json:"refused"`
}

// Skip is a path left for the next sync, and why.
type Skip struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

// fetchRequest is what _store-send reads.
type fetchRequest struct {
	Paths []string `json:"paths"`
}

// ServeOffer is the target side of an offer: scan, then print the manifest.
func ServeOffer(s *Store, out io.Writer, from string) error {
	if err := s.Lock(PeerLockWait); err != nil {
		return err
	}
	defer s.Unlock()
	changed, refused, err := s.Scan()
	if err != nil {
		return err
	}
	if err := s.Save(); err != nil {
		return err
	}
	if len(changed) > 0 && s.Kind.AfterChange != nil {
		s.Kind.AfterChange(s, changed)
	}
	return json.NewEncoder(out).Encode(s.manifest(refused, from))
}

// ServeSend writes a bundle of the asked-for paths as they are now.
func ServeSend(s *Store, in io.Reader, out io.Writer) error {
	var req fetchRequest
	if err := json.NewDecoder(io.LimitReader(in, 16<<20)).Decode(&req); err != nil {
		return fmt.Errorf("bad request: %w", err)
	}
	if err := s.Lock(PeerLockWait); err != nil {
		return err
	}
	defer s.Unlock()
	st, err := s.State()
	if err != nil {
		return err
	}
	b := Bundle{Store: s.Kind.Name, From: s.Self, Files: map[string]FileState{}, Data: map[string][]byte{}}
	for _, p := range req.Paths {
		f, ok := st.Files[p]
		if !ok {
			continue
		}
		if !f.Deleted {
			data := s.content(p)
			if data == nil {
				continue // changed since the scan; the next sync gets it
			}
			b.Data[p] = data
		}
		b.Files[p] = f
	}
	return WriteBundle(out, b)
}

// ServeRecv applies a bundle a peer pushed. from is the verified sender,
// when there is one; else the bundle's own claim.
func ServeRecv(s *Store, in io.Reader, out io.Writer, from string) error {
	b, err := ReadBundle(in)
	if err != nil {
		return err
	}
	if b.Store != s.Kind.Name {
		return fmt.Errorf("bundle is for store %q, not %q", b.Store, s.Kind.Name)
	}
	if from == "" {
		from = b.From
	}
	if err := s.Lock(PeerLockWait); err != nil {
		return err
	}
	defer s.Unlock()
	scanned, _, err := s.Scan()
	if err != nil {
		return err
	}
	res := PushResult{Applied: []string{}, Pending: []string{}, Skipped: []Skip{}, Refused: []Refusal{}}
	paths := make([]string, 0, len(b.Files))
	for p := range b.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var changed []string
	for _, p := range b.Hold {
		if f, ok := s.st.Files[p]; ok && !f.Deleted && ValidRel(p) == nil {
			s.hold(p)
		}
	}
	for _, p := range paths {
		delete(s.st.Held, p) // the sender knows it
		what, err := s.apply(p, b.Files[p], b.Data[p], from, false)
		var re refusedError
		switch {
		case errors.As(err, &re):
			re.r.Path = p
			res.Refused = append(res.Refused, re.r)
		case err != nil:
			res.Skipped = append(res.Skipped, Skip{Path: p, Why: err.Error()})
		case what == "pending":
			res.Pending = append(res.Pending, p)
			changed = append(changed, p)
		case what != "":
			res.Applied = append(res.Applied, p)
			changed = append(changed, p)
		}
	}
	if from != "" {
		s.st.Peers[from] = s.now()
	}
	if err := s.Save(); err != nil {
		return err
	}
	changed = append(changed, scanned...)
	if len(changed) > 0 && s.Kind.AfterChange != nil {
		s.Kind.AfterChange(s, changed)
	}
	return json.NewEncoder(out).Encode(res)
}
