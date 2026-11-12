package secrets

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func scrubAll(values map[string]string, chunks ...string) string {
	var out bytes.Buffer
	s := NewScrubber(&out, values)
	for _, c := range chunks {
		if _, err := s.Write([]byte(c)); err != nil {
			panic(err)
		}
	}
	if err := s.Flush(); err != nil {
		panic(err)
	}
	return out.String()
}

func TestScrubBasic(t *testing.T) {
	got := scrubAll(map[string]string{"API_KEY": "s3cr3t-value"}, "key=s3cr3t-value and again s3cr3t-value\n")
	if got != "key=[API_KEY] and again [API_KEY]\n" {
		t.Fatalf("got %q", got)
	}
}

func TestScrubStraddlesEverySplit(t *testing.T) {
	vals := map[string]string{"TOKEN": "abcdef123456"}
	in := "before abcdef123456 middle abcdef123456 after"
	want := "before [TOKEN] middle [TOKEN] after"
	for i := 0; i <= len(in); i++ {
		for j := i; j <= len(in); j++ {
			if got := scrubAll(vals, in[:i], in[i:j], in[j:]); got != want {
				t.Fatalf("split %d/%d: got %q", i, j, got)
			}
		}
	}
}

func TestScrubByteByByte(t *testing.T) {
	vals := map[string]string{"A": "hunter2", "B": "xyzzy"}
	in := "hhunter2 xyzzxyzzy hunter hunter2"
	chunks := strings.Split(in, "")
	got := scrubAll(vals, chunks...)
	if got != "h[A] xyzz[B] hunter [A]" {
		t.Fatalf("got %q", got)
	}
}

func TestScrubOverlapping(t *testing.T) {
	// one value is a prefix of another: the longer one wins, even split across writes
	vals := map[string]string{"SHORT": "pass", "LONG": "password99"}
	if got := scrubAll(vals, "x pass", "word99 y pass z"); got != "x [LONG] y [SHORT] z" {
		t.Fatalf("got %q", got)
	}
	if got := scrubAll(vals, "end pass"); got != "end [SHORT]" {
		t.Fatalf("flush should still scrub a held match: %q", got)
	}
}

func TestScrubHoldsOnlyPossiblePrefixes(t *testing.T) {
	var out bytes.Buffer
	s := NewScrubber(&out, map[string]string{"K": "secret"})
	_, _ = s.Write([]byte("hello sec"))
	if out.String() != "hello " {
		t.Fatalf("should emit everything but the possible prefix, got %q", out.String())
	}
	_, _ = s.Write([]byte("ond\n"))
	if out.String() != "hello second\n" {
		t.Fatalf("got %q", out.String())
	}
}

func TestScrubNoSecretsPassthrough(t *testing.T) {
	if got := scrubAll(nil, "a", "b"); got != "ab" {
		t.Fatal(got)
	}
	if got := scrubAll(map[string]string{"EMPTY": ""}, "a", "b"); got != "ab" {
		t.Fatal("empty values are ignored")
	}
}

func TestResolveNoProvider(t *testing.T) {
	t.Setenv("YONDER_HOME", t.TempDir())
	if m, err := Resolve(nil); err != nil || len(m) != 0 {
		t.Fatal("no names should resolve to nothing")
	}
	if _, err := Resolve([]string{"X"}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("want ErrNoProvider, got %v", err)
	}
}

func TestScrubOverlappingValuesNeverLeak(t *testing.T) {
	vals := map[string]string{"A": "abc", "B": "bcdefgh"}
	in := "zabcdefghz"
	want := "z[A+B]z"
	for i := 0; i <= len(in); i++ {
		for j := i; j <= len(in); j++ {
			if got := scrubAll(vals, in[:i], in[i:j], in[j:]); got != want {
				t.Fatalf("split %d/%d: got %q", i, j, got)
			}
		}
	}
	if got := scrubAll(vals, strings.Split(in, "")...); got != want {
		t.Fatalf("byte by byte: %q", got)
	}
	// map order must not matter for the one-shot form either
	for range 20 {
		if got := ScrubText(in, vals); got != want {
			t.Fatalf("ScrubText: %q", got)
		}
	}
	// a value that repeats into itself is one span
	if got := ScrubText("xabcabcabx", map[string]string{"R": "abcab"}); got != "x[R]x" {
		t.Fatalf("self overlap: %q", got)
	}
}

// the value as a program might print it without meaning to transform it.
func TestScrubValueForms(t *testing.T) {
	vals := map[string]string{"K": "sk-line-one-123\nsk-line-two-456\n", "Q": `pa"ss<w>rd\`}
	cases := map[string]string{
		"trimmed":   "x sk-line-one-123\nsk-line-two-456 y",
		"one line":  "got sk-line-two-456 here",
		"json":      `{"v":"sk-line-one-123\nsk-line-two-456\n"}`,
		"json html": `"pa\"ss\u003cw\u003erd\\"`,
		"json raw":  `"pa\"ss<w>rd\\"`,
	}
	for name, in := range cases {
		got := ScrubText(in, vals)
		if strings.Contains(got, "sk-line") || strings.Contains(got, "ss<w>") || strings.Contains(got, "u003cw") {
			t.Errorf("%s: %q", name, got)
		}
		if got2 := scrubAll(vals, strings.Split(in, "")...); got2 != got {
			t.Errorf("%s: stream %q != text %q", name, got2, got)
		}
	}
	// short lines of a multi-line value are left alone
	if got := ScrubText("ok then", map[string]string{"K": "ok\nlong-enough-line"}); got != "ok then" {
		t.Fatalf("short line scrubbed: %q", got)
	}
}

// a short value trimmed is too common to scrub alone; the exact value still is.
func TestScrubShortTrimmedForm(t *testing.T) {
	vals := map[string]string{"K": " a\n"}
	if got := ScrubText("a banana", vals); got != "a banana" {
		t.Fatalf("trimmed short value over-redacted: %q", got)
	}
	if got := ScrubText("x a\ny", vals); got != "x[K]y" {
		t.Fatalf("exact value kept: %q", got)
	}
	if got := ScrubText("ab", map[string]string{"K": "a"}); got != "[K]b" {
		t.Fatalf("short exact value kept: %q", got)
	}
	// a long enough value is still caught trimmed
	if got := ScrubText("[sk-abcdefgh]", map[string]string{"K": " sk-abcdefgh\n"}); got != "[[K]]" {
		t.Fatalf("trimmed form missed: %q", got)
	}
}
