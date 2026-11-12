package mesh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zacharytgray/yonder/internal/transport"
)

// a peer's authorized_keys is read and replaced with plain sh, so mesh and
// remove work whatever yonder version runs there. the edit itself happens
// here; the write only goes through if the file is unchanged since the read.

// home "" means the target's $HOME.
func homeVar(home string) string {
	if home == "" {
		return `h="$HOME"`
	}
	return "h=" + transport.Quote(home)
}

const readBody = `
d="$h/.ssh"; f="$d/authorized_keys"
link=
if [ -L "$d" ] || [ -L "$f" ]; then
  link=$(realpath "$f" 2>/dev/null || readlink "$f" 2>/dev/null || echo "$f")
fi
if [ -f "$f" ]; then echo "sum=$(cksum < "$f")"; else echo "sum=missing"; fi
echo "link=$link"
echo "--"
if [ -f "$f" ]; then cat "$f"; fi
exit 0`

// exit 3: symlink, exit 4: changed since the read
const writeBody = `
d="$h/.ssh"; f="$d/authorized_keys"
if [ -L "$d" ] || [ -L "$f" ]; then echo "$f is a symlink; yonder won't rewrite it" >&2; exit 3; fi
if [ -f "$f" ]; then now=$(cksum < "$f"); else now=missing; fi
if [ "$now" != "$want" ]; then echo "$f changed while yonder was editing it" >&2; exit 4; fi
if [ ! -d "$d" ]; then mkdir -p "$d" && chmod 700 "$d" || exit 1; fi
t="$d/.yonder-tmp-$$"
if [ -f "$f" ]; then
  n="$(date +%s)000000000"
  while [ -e "$f.yonder-bak.$n" ]; do n=$((n+1)); done
  cp -p "$f" "$f.yonder-bak.$n" || exit 1
  cp -p "$f" "$t" && cat > "$t" || { rm -f "$t"; exit 1; }
  echo "backup=$f.yonder-bak.$n"
else
  (umask 077 && cat > "$t") || { rm -f "$t"; exit 1; }
fi
mv -f "$t" "$f" || { rm -f "$t"; exit 1; }`

// File is one authorized_keys as read from a machine.
type File struct {
	Content string
	Link    string // the real file, when it or ~/.ssh is a symlink
	sum     string
}

// ErrChanged is a write refused because the file changed after the read.
var ErrChanged = errors.New("authorized_keys changed while yonder was editing it")

// Read reads ~/.ssh/authorized_keys on t (missing reads as empty).
func Read(ctx context.Context, r transport.Runner, t transport.Target, home string) (File, error) {
	var out, errb bytes.Buffer
	code, err := r.Shell(ctx, t, homeVar(home)+readBody, transport.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb})
	if err != nil {
		return File{}, err
	}
	if code != 0 {
		return File{}, fmt.Errorf("reading authorized_keys: exit %d: %s", code, strings.TrimSpace(errb.String()))
	}
	head, body, ok := strings.Cut(out.String(), "\n--\n")
	if !ok {
		return File{}, fmt.Errorf("reading authorized_keys: unexpected output %q", out.String())
	}
	var f File
	for _, l := range strings.Split(head, "\n") {
		k, v, _ := strings.Cut(l, "=")
		switch k {
		case "sum":
			f.sum = v
		case "link":
			f.Link = v
		}
	}
	if f.sum == "" {
		return File{}, fmt.Errorf("reading authorized_keys: unexpected output %q", out.String())
	}
	f.Content = body
	return f, nil
}

// Write replaces the file f was read from with content, after a backup.
// it refuses a symlink, and returns ErrChanged if the file moved on since Read.
func Write(ctx context.Context, r transport.Runner, t transport.Target, home string, f File, content string) (backup string, err error) {
	var out, errb bytes.Buffer
	script := homeVar(home) + "\nwant=" + transport.Quote(f.sum) + writeBody
	code, err := r.Shell(ctx, t, script, transport.IO{Stdin: strings.NewReader(content), Stdout: &out, Stderr: &errb})
	if err != nil {
		return "", err
	}
	switch code {
	case 0:
	case 4:
		return "", ErrChanged
	default:
		return "", fmt.Errorf("writing authorized_keys: exit %d: %s", code, strings.TrimSpace(errb.String()))
	}
	for _, l := range strings.Split(out.String(), "\n") {
		if b, ok := strings.CutPrefix(l, "backup="); ok {
			backup = b
		}
	}
	return backup, nil
}

// Edit reads, edits and writes back t's authorized_keys, retrying once if it
// changed in between. a symlinked file is never written: that is a
// *transport.SymlinkError with the lines to change by hand.
func Edit(ctx context.Context, r transport.Runner, t transport.Target, home string, add bool, edit func(string) (string, []string)) (lines []string, backup string, err error) {
	for try := 0; ; try++ {
		f, err := Read(ctx, r, t, home)
		if err != nil {
			return nil, "", err
		}
		content, lines := edit(f.Content)
		if len(lines) == 0 {
			return nil, "", nil
		}
		if f.Link != "" {
			return nil, "", &transport.SymlinkError{Path: "~/.ssh/authorized_keys", Real: f.Link, Lines: lines, Add: add}
		}
		backup, err := Write(ctx, r, t, home, f, content)
		if errors.Is(err, ErrChanged) && try == 0 {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return lines, backup, nil
	}
}
