package remote

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/runlog"
)

// CopySpec rides in the envelope for `yonder cp`. _send reads Sources, _recv reads Dest.
type CopySpec struct {
	Sources []string `json:"sources,omitempty"`
	Dest    string   `json:"dest,omitempty"`
	Force   bool     `json:"force,omitempty"`
}

// CopyResult is the one JSON line _recv prints on success.
type CopyResult struct {
	Files   int        `json:"files"`
	Bytes   int64      `json:"bytes"`
	Items   []CopyItem `json:"items"`
	Skipped []string   `json:"skipped"`
}

type CopyItem struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

// ExitCutOff is _recv's exit code when the stream ended before the sender's end
// marker: the cause is on the sending side.
const ExitCutOff = 74

// the last entry of every cp archive. without it _recv keeps nothing.
const doneMarker = ".yonder-cp-done"

type copyTotals struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

func readCopyEnvelope(name string, in io.Reader, errw io.Writer) (Envelope, *bufio.Reader, bool) {
	br := bufio.NewReaderSize(in, 64<<10)
	env, err := ReadEnvelope(br)
	if err == nil && env.Copy == nil {
		err = errors.New("envelope has no copy")
	}
	if err != nil {
		fmt.Fprintf(errw, "yonder %s: %v\n", name, err)
		return env, nil, false
	}
	return env, br, true
}

// expandPath expands a leading ~ to this machine's home. relative paths stay
// relative to the working dir, which over ssh is the home.
func expandPath(p string) string {
	return filepath.Clean(registry.ExpandHome(p))
}

// Send is `yonder _send`: envelope on stdin, a tar of copy.sources on stdout.
func Send(ctx context.Context, _ []string, in io.Reader, out, errw io.Writer) int {
	env, _, ok := readCopyEnvelope("_send", in, errw)
	if !ok {
		return 1
	}
	srcs, err := sendSources(env.Copy.Sources)
	if err == nil {
		err = writeTar(ctx, srcs, out, errw)
	}
	if err != nil {
		fmt.Fprintf(errw, "yonder: %v\n", err)
		return 1
	}
	return 0
}

type source struct{ name, path string }

// sendSources checks every source before a byte is sent. a symlink given as a
// source is followed; links inside a folder are sent as links.
func sendSources(paths []string) ([]source, error) {
	if len(paths) == 0 {
		return nil, errors.New("nothing to copy")
	}
	seen := map[string]bool{}
	var out []source
	for _, p := range paths {
		abs, err := filepath.Abs(expandPath(p))
		if err != nil {
			return nil, err
		}
		name := filepath.Base(abs)
		if name == "/" || name == "." || name == doneMarker {
			return nil, fmt.Errorf("can't copy %s", p)
		}
		real, err := filepath.EvalSymlinks(abs)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no such file or folder: %s", p)
		}
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(real)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a file or folder", p)
		}
		if seen[name] {
			return nil, fmt.Errorf("two sources are named %s; copy them one at a time", name)
		}
		seen[name] = true
		out = append(out, source{name: name, path: real})
	}
	return out, nil
}

func writeTar(ctx context.Context, srcs []source, out, errw io.Writer) error {
	tw := tar.NewWriter(out)
	var tot copyTotals
	for _, s := range srcs {
		err := filepath.WalkDir(s.path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			rel, err := filepath.Rel(s.path, p)
			if err != nil {
				return err
			}
			name := s.name
			if rel != "." {
				name = path.Join(s.name, filepath.ToSlash(rel))
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			link := ""
			switch m := info.Mode(); {
			case m.IsRegular(), m.IsDir():
			case m&fs.ModeSymlink != 0:
				if link, err = os.Readlink(p); err != nil {
					return err
				}
			default:
				fmt.Fprintf(errw, "yonder: skipped %s (not a file, folder or link)\n", p)
				return nil
			}
			hdr, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			hdr.Name = name
			if info.IsDir() {
				hdr.Name += "/"
			}
			// no owner names or ids leave this machine
			hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
			hdr.AccessTime, hdr.ChangeTime = time.Time{}, time.Time{}
			hdr.Mode &= 0o777
			hdr.Format = tar.FormatPAX
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			n, err := io.CopyN(tw, f, hdr.Size)
			if err != nil {
				return fmt.Errorf("read %s: %w", p, err)
			}
			tot.Files++
			tot.Bytes += n
			return nil
		})
		if err != nil {
			return err
		}
	}
	data, _ := json.Marshal(tot)
	hdr := &tar.Header{Name: doneMarker, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(data)), Format: tar.FormatPAX}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	return tw.Close()
}

// Recv is `yonder _recv`: envelope line, then the tar from _send. it logs the
// copy like _exec logs a command: a start entry, then the completion.
func Recv(ctx context.Context, _ []string, in io.Reader, out, errw io.Writer) int {
	env, br, ok := readCopyEnvelope("_recv", in, errw)
	if !ok {
		return 1
	}
	fillDefaults(&env)
	start := time.Now()
	s := entry(env, start)
	s.Copy = true
	s.Status = runlog.StatusStarted
	if err := runlog.Append(registry.LogPath(), s); err != nil {
		fmt.Fprintf(errw, "yonder: could not write log: %v\n", err)
	}
	res, code, err := receive(ctx, *env.Copy, br, errw)
	e := entry(env, start)
	e.Copy = true
	e.Exit = runlog.Exit(code)
	if err != nil {
		e.Error = err.Error()
		fmt.Fprintf(errw, "yonder: %v\n", err)
	} else {
		e.Files, e.Bytes = res.Files, res.Bytes
	}
	if err := runlog.Append(registry.LogPath(), e); err != nil {
		fmt.Fprintf(errw, "yonder: could not write log: %v\n", err)
	}
	if err == nil {
		_ = json.NewEncoder(out).Encode(res)
	}
	return code
}

type topItem struct {
	name, final string
	dir         bool
}

var errCutOff = errors.New("the transfer ended early; nothing was copied")

// receive unpacks into a hidden staging folder next to the destination, under
// os.Root so no entry can land outside it, then renames each item into place.
func receive(ctx context.Context, spec CopySpec, r io.Reader, errw io.Writer) (CopyResult, int, error) {
	res := CopyResult{Items: []CopyItem{}, Skipped: []string{}}
	if spec.Dest == "" {
		return res, 1, errors.New("no destination")
	}
	dest := expandPath(spec.Dest)
	st, err := os.Stat(dest)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, 1, err
	}
	exists := err == nil
	into := strings.HasSuffix(spec.Dest, "/") || (exists && st.IsDir())
	dir := dest
	switch {
	case into && exists && !st.IsDir():
		return res, 1, fmt.Errorf("%s is not a folder", dest)
	case into && !exists:
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return res, 1, err
		}
	case !into:
		dir = filepath.Dir(dest)
		if pst, err := os.Stat(dir); err != nil || !pst.IsDir() {
			return res, 1, fmt.Errorf("no folder %s to copy into", dir)
		}
		if exists && !spec.Force {
			return res, 1, fmt.Errorf("%s already exists; add --force to replace it", dest)
		}
	}
	stage, err := os.MkdirTemp(dir, ".yonder-cp-")
	if err != nil {
		return res, 1, err
	}
	defer os.RemoveAll(stage)
	root, err := os.OpenRoot(stage)
	if err != nil {
		return res, 1, err
	}
	defer root.Close()

	var tops []topItem
	topSeen := map[string]bool{}
	dirs := map[string]bool{} // folders made from the archive; nothing is written through anything else
	var done *copyTotals
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return res, 1, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return res, ExitCutOff, fmt.Errorf("%w (%v)", errCutOff, err)
		}
		if done != nil {
			return res, 1, errors.New("refused: the archive goes on after its end marker; nothing was copied")
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if name == doneMarker {
			var t copyTotals
			if err := json.NewDecoder(io.LimitReader(tr, 4096)).Decode(&t); err != nil {
				return res, 1, fmt.Errorf("bad end marker: %v", err)
			}
			done = &t
			continue
		}
		if !safeName(name) {
			return res, 1, fmt.Errorf("refused: unsafe path %q in the archive; nothing was copied", hdr.Name)
		}
		top, _, nested := strings.Cut(name, "/")
		switch {
		case !topSeen[top]:
			if nested {
				return res, 1, fmt.Errorf("refused: %q comes before its folder; nothing was copied", hdr.Name)
			}
			if !into && len(tops) == 1 {
				return res, 1, errors.New("several items need a folder to go into; end the destination with /")
			}
			final := dest
			if into {
				final = filepath.Join(dest, top)
			}
			if _, err := os.Lstat(final); err == nil && !spec.Force {
				return res, 1, fmt.Errorf("%s already exists; add --force to replace it", final)
			}
			topSeen[top] = true
			tops = append(tops, topItem{name: top, final: final, dir: hdr.Typeflag == tar.TypeDir})
		case !nested || !dirs[path.Dir(name)]:
			return res, 1, fmt.Errorf("refused: %q is not inside a folder from the archive; nothing was copied", hdr.Name)
		}
		mode := fs.FileMode(hdr.Mode) & 0o777
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.Mkdir(name, mode|0o700); err != nil {
				return res, 1, err
			}
			dirs[name] = true
		case tar.TypeReg:
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode|0o600)
			if err != nil {
				return res, 1, err
			}
			n, err := io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return res, ExitCutOff, fmt.Errorf("%w (%v)", errCutOff, err)
			}
			_ = root.Chtimes(name, hdr.ModTime, hdr.ModTime)
			res.Files++
			res.Bytes += n
		case tar.TypeSymlink:
			if !linkInside(name, hdr.Linkname) {
				fmt.Fprintf(errw, "yonder: skipped link %s -> %s (it points outside the copied folder)\n", name, hdr.Linkname)
				res.Skipped = append(res.Skipped, name)
				continue
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return res, 1, err
			}
		default:
			fmt.Fprintf(errw, "yonder: skipped %s (not a file, folder or link)\n", name)
			res.Skipped = append(res.Skipped, name)
		}
	}
	if done == nil {
		return res, ExitCutOff, errCutOff
	}
	if done.Files != res.Files || done.Bytes != res.Bytes {
		return res, ExitCutOff, fmt.Errorf("%w (got %d files, %d bytes; sender sent %d, %d)", errCutOff, res.Files, res.Bytes, done.Files, done.Bytes)
	}
	if len(tops) == 0 {
		return res, 1, errors.New("nothing to copy")
	}
	root.Close()
	for _, t := range tops {
		src := filepath.Join(stage, t.name)
		if _, err := os.Lstat(src); err != nil {
			continue // its only entry was skipped
		}
		if err := place(src, t.final, stage, spec.Force); err != nil {
			if len(res.Items) > 0 {
				err = fmt.Errorf("%v (already copied: %s)", err, res.Items[0].Path)
			}
			return res, 1, err
		}
		res.Items = append(res.Items, CopyItem{Path: t.final, Dir: t.dir})
	}
	return res, 0, nil
}

// place renames src to final. with force, an existing final is moved aside
// first and put back if the rename fails.
func place(src, final, stage string, force bool) error {
	if _, err := os.Lstat(final); err != nil {
		return os.Rename(src, final)
	}
	if !force {
		return fmt.Errorf("%s already exists; add --force to replace it", final)
	}
	// made after unpacking, so it can't collide with an item
	aside, err := os.MkdirTemp(stage, ".replaced-")
	if err != nil {
		return err
	}
	old := filepath.Join(aside, "old")
	if err := os.Rename(final, old); err != nil {
		return err
	}
	if err := os.Rename(src, final); err != nil {
		_ = os.Rename(old, final)
		return err
	}
	return nil
}

// safeName accepts only plain relative paths: no leading /, no empty, . or .. parts.
func safeName(name string) bool {
	if name == "" || strings.ContainsRune(name, 0) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// linkInside reports whether a link at name with this target stays inside the
// item it belongs to. a .. after a normal part is refused: if that part is
// itself a link, the system resolves it before going up, and a lexical check
// can't follow that.
func linkInside(name, target string) bool {
	if target == "" || path.IsAbs(target) || strings.ContainsRune(target, 0) {
		return false
	}
	down := false
	for _, part := range strings.Split(target, "/") {
		switch part {
		case "..":
			if down {
				return false
			}
		case "", ".":
		default:
			down = true
		}
	}
	top, _, nested := strings.Cut(name, "/")
	if !nested {
		return false // a link as the item itself would resolve beside it
	}
	full := path.Join(path.Dir(name), target)
	return full == top || strings.HasPrefix(full, top+"/")
}
