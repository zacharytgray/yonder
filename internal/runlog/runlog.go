package runlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	SideTarget = "target" // logged by the machine that ran the command
	SideCaller = "caller" // logged by the caller when it couldn't reach the target
)

// Entry is one line of log.jsonl.
type Entry struct {
	Time     time.Time `json:"time"`
	ID       string    `json:"id"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Harness  string    `json:"harness"`
	User     string    `json:"user"`
	Command  string    `json:"command"`
	Secrets  []string  `json:"secrets"`
	Exit     *int      `json:"exit"` // null for a detached launch
	MS       int64     `json:"ms"`
	Side     string    `json:"side"`
	Detached bool      `json:"detached,omitempty"`
	Error    string    `json:"error,omitempty"`
}

func Exit(code int) *int { return &code }

// Append writes one entry as a single O_APPEND write.
func Append(path string, e Entry) error {
	if e.Secrets == nil {
		e.Secrets = []string{}
	}
	if e.Side == "" {
		e.Side = SideTarget
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Read returns entries at or after since (zero means all), oldest first.
// unparseable lines are skipped.
func Read(path string, since time.Time) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []Entry{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		out = append(out, e)
	}
	Sort(out)
	return out, sc.Err()
}

// Sort orders entries by time, then id.
func Sort(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if !es[i].Time.Equal(es[j].Time) {
			return es[i].Time.Before(es[j].Time)
		}
		return es[i].ID < es[j].ID
	})
}
