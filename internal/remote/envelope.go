package remote

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"time"
)

const envelopeVersion = 1

// Envelope is the first stdin line of `yonder _exec`. everything after it is
// the caller's stdin, passed through to the command.
type Envelope struct {
	V       int               `json:"v"`
	ID      string            `json:"id"`
	Command string            `json:"command"` // run with $SHELL -lc
	From    string            `json:"from"`
	To      string            `json:"to"`
	Harness string            `json:"harness"`
	Secrets map[string]string `json:"secrets,omitempty"` // name -> value
	Detach  bool              `json:"detach,omitempty"`
}

const maxEnvelope = 1 << 20

// Encode returns the envelope as one JSON line.
func (e Envelope) Encode() ([]byte, error) {
	if e.V == 0 {
		e.V = envelopeVersion
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// SecretNames returns the sorted names, never values.
func (e Envelope) SecretNames() []string {
	names := make([]string, 0, len(e.Secrets))
	for n := range e.Secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ReadEnvelope reads exactly one line from r. r keeps the rest for the command.
func ReadEnvelope(r *bufio.Reader) (Envelope, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				break
			}
			return Envelope{}, fmt.Errorf("read envelope: %w", err)
		}
		line = append(line, chunk...)
		if len(line) > maxEnvelope {
			return Envelope{}, errors.New("envelope too large")
		}
		if !isPrefix {
			break
		}
	}
	var e Envelope
	if err := json.Unmarshal(line, &e); err != nil {
		return Envelope{}, fmt.Errorf("parse envelope: %w", err)
	}
	if e.V < 1 {
		return Envelope{}, errors.New("envelope has no version")
	}
	if e.V > envelopeVersion {
		return Envelope{}, fmt.Errorf("envelope version %d is newer than this yonder understands (%d); upgrade yonder here", e.V, envelopeVersion)
	}
	if e.Command == "" {
		return Envelope{}, errors.New("envelope has no command")
	}
	return e, nil
}

// NewID returns a sortable, filename-safe run id.
func NewID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// ValidID reports whether id is safe to use as a file name.
func ValidID(id string) bool { return idRe.MatchString(id) }
