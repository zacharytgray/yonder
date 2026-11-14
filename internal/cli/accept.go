package cli

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/store"
)

// the terminal check, the questions accept asks and its pager; tests swap them
var (
	acceptTTY = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) }
	acceptAsk = func(q string) bool {
		ok := false
		if err := huh.NewConfirm().Title(q).Affirmative("Accept").Negative("Not now").Value(&ok).Run(); err != nil {
			return false
		}
		return ok
	}
	acceptChoose = func(title string, opts []string) string {
		var pick string
		var hs []huh.Option[string]
		for _, o := range opts {
			hs = append(hs, huh.NewOption(o, o))
		}
		if err := huh.NewSelect[string]().Title(title).Options(hs...).Value(&pick).Run(); err != nil {
			return ""
		}
		return pick
	}
	acceptPage = func(w io.Writer, text string) {
		pager := os.Getenv("PAGER")
		if pager == "" {
			pager = "less"
		}
		if f, ok := w.(*os.File); ok && isTerminal(f) {
			if p, err := exec.LookPath(strings.Fields(pager)[0]); err == nil {
				args := strings.Fields(pager)[1:]
				if filepath.Base(p) == "less" && len(args) == 0 {
					args = []string{"-R"}
				}
				c := exec.Command(p, args...)
				c.Stdin, c.Stdout, c.Stderr = strings.NewReader(text), f, os.Stderr
				if c.Run() == nil {
					return
				}
			}
		}
		fmt.Fprint(w, text)
	}
)

// the choices at the prompt
const (
	choiceAccept = "Accept all"
	choiceShow   = "Show the full diffs first"
	choiceOne    = "Go through them one by one"
	choiceNo     = "Not now"
)

// ticketTTL is how long a target honors the list it gave a caller
const ticketTTL = 10 * time.Minute

// acceptItem is one waiting change, as the summary shows it.
type acceptItem struct {
	Path    string       `json:"path"`
	Kind    string       `json:"kind"` // hooks, scripts, harness files, other
	Origin  store.Origin `json:"origin"`
	Since   time.Time    `json:"since"`
	New     bool         `json:"new"`
	Lines   int          `json:"lines"`   // of the incoming file
	Added   int          `json:"added"`   // lines, against what is live
	Removed int          `json:"removed"` // lines
	Bytes   int64        `json:"bytes"`
	Mode    string       `json:"mode,omitempty"` // the exec bit's change
	Hash    string       `json:"hash"`
	Exec    bool         `json:"exec"`
	Diff    string       `json:"diff"`
}

// acceptList is what waits on one machine. Ticket is that machine's promise
// to accept exactly these, for the caller that asked, for a short while.
type acceptList struct {
	Machine string       `json:"machine"`
	Ticket  string       `json:"ticket,omitempty"`
	Items   []acceptItem `json:"items"`
	Error   string       `json:"-"` // caller side: why it wasn't read
	self    bool
}

// acceptWant is what the caller asks a target to accept: what it listed.
type acceptWant struct {
	Ticket string     `json:"ticket"`
	Items  []wantItem `json:"items"`
}

type wantItem struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Exec bool   `json:"exec"`
}

type acceptDone struct {
	Accepted []string `json:"accepted"`
	Skipped  []skip   `json:"skipped"`
}

type skip struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

func reviewKind(rel string, exec bool) string {
	p := rel
	if rest, ok := strings.CutPrefix(p, "machines/"); ok {
		if _, after, ok := strings.Cut(rest, "/"); ok {
			p = after
		}
	}
	switch {
	case strings.HasPrefix(p, "hooks/"):
		return "hooks"
	case strings.HasPrefix(p, "files/"):
		return "harness files"
	case exec:
		return "scripts"
	}
	return "other"
}

var kindOrder = []string{"hooks", "scripts", "harness files", "other"}

// listPending reads what waits in s. callers hold no lock.
func listPending(s *store.Store) ([]acceptItem, error) {
	if err := s.Lock(30 * time.Second); err != nil {
		return nil, err
	}
	defer s.Unlock()
	items, err := s.PendingList()
	if err != nil {
		return nil, err
	}
	out := []acceptItem{}
	for _, it := range items {
		live, in, err := s.PendingData(it.Path)
		if err != nil {
			continue
		}
		ai := acceptItem{Path: it.Path, Kind: reviewKind(it.Path, it.State.Exec), Origin: it.State.Origin, Since: it.Pending.Since,
			New: !it.Pending.LiveExists, Bytes: int64(len(in)), Hash: it.State.Hash, Exec: it.State.Exec,
			Mode: strings.TrimSpace(agents.ModeChange(it.Pending.LiveExists && it.Pending.LiveExec, it.State.Exec)),
			Diff: pendingDiff(it, live, in)}
		ai.Lines = len(strings.Split(strings.TrimSuffix(string(in), "\n"), "\n"))
		for _, l := range strings.Split(ai.Diff, "\n") {
			switch {
			case strings.HasPrefix(l, "+++ ") || strings.HasPrefix(l, "--- "):
			case strings.HasPrefix(l, "+"):
				ai.Added++
			case strings.HasPrefix(l, "-"):
				ai.Removed++
			}
		}
		out = append(out, ai)
	}
	return out, nil
}

func newAgentsAcceptCmd() *cobra.Command {
	var all, allMachines, show bool
	cmd := &cobra.Command{
		Use:   "accept [path...] [--all] [--show] [--all-machines]",
		Short: "Review incoming hooks and other executables, and make them live",
		Long: `Hooks run code, so a new or changed one from another machine waits here until you
accept it: it syncs on to the fleet, but nothing runs it on this machine yet.

  yonder agents accept                 one summary of everything waiting here, grouped
  yonder agents accept --all           into hooks, scripts and harness files (path, the
                                       machine it came from, how long ago, lines changed);
                                       one confirmation accepts all of it
  yonder agents accept --show          page through the full diffs before the question
  yonder agents accept --all-machines  the same summary for every online machine, and one
                                       confirmation here accepts on each of them
  yonder agents accept <path>...       ask about each path named, one at a time

It needs a person at a terminal, so an agent can't accept for you. Only what the summary
showed is accepted: a change that arrives meanwhile keeps waiting.

A change a person already reviewed on another machine (accepted there, or imported there
at a terminal) doesn't wait again. An edit an agent made on another machine always waits.

Which changes wait is [sync] review in ~/.config/yonder/config.toml: "executables" (the
default: hooks, executable files, hook configs), "all", or "none".`,
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			if len(args) > 0 && (all || allMachines) {
				return &usageError{errors.New("name paths, or use --all; not both")}
			}
			if len(args) > 0 {
				return a.acceptPaths(args)
			}
			if allMachines {
				return a.acceptFleet(show)
			}
			return a.acceptAll(show)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "one summary and one confirmation for everything waiting (the default without paths)")
	cmd.Flags().BoolVar(&show, "show", false, "page through the full diffs before asking")
	cmd.Flags().BoolVar(&allMachines, "all-machines", false, "every online machine, from this terminal")
	return cmd
}

// notTerminal is the refusal when no person is at a terminal.
func notTerminal(fleet bool) error {
	c := "yonder agents accept --all"
	if fleet {
		c += " --all-machines"
	}
	return fmt.Errorf("accept needs a person at a terminal, so it accepts nothing here. Ask the user to run `%s` in a terminal; `yonder agents diff <path>` shows one change", c)
}

// acceptAll is the summary and one confirmation, for this machine.
func (a *app) acceptAll(show bool) error {
	s, _, err := a.agentStore("agents accept")
	if err != nil {
		return err
	}
	items, err := listPending(s)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(a.out, "nothing waits for review here")
		return nil
	}
	l := acceptList{Machine: "this machine", Items: items, self: true}
	fmt.Fprintf(a.out, "%s wait%s for review here:\n", plural(len(items), "change"), verbS(len(items)))
	printSummary(a.out, []acceptList{l}, false)
	if !acceptTTY() {
		return notTerminal(false)
	}
	if show {
		acceptPage(a.out, diffsText([]acceptList{l}))
	}
	for {
		switch acceptChoose(fmt.Sprintf("Accept all %s on this machine?", plural(len(items), "change")), []string{choiceAccept, choiceShow, choiceOne, choiceNo}) {
		case choiceAccept:
			done := a.acceptHere(s, items)
			a.printDone("", done)
			return nil
		case choiceShow:
			acceptPage(a.out, diffsText([]acceptList{l}))
		case choiceOne:
			var paths []string
			for _, it := range items {
				paths = append(paths, it.Path)
			}
			return a.acceptPaths(paths)
		default:
			fmt.Fprintln(a.out, "nothing accepted; it all still waits")
			return nil
		}
	}
}

func verbS(n int) string {
	if n == 1 {
		return "s"
	}
	return ""
}

// acceptHere accepts the listed items in s, only as they were listed.
func (a *app) acceptHere(s *store.Store, items []acceptItem) acceptDone {
	done := acceptDone{Accepted: []string{}, Skipped: []skip{}}
	_, err := withStore(s, func() error {
		for _, it := range items {
			if err := s.AcceptIf(it.Path, it.Hash, it.Exec); err != nil {
				done.Skipped = append(done.Skipped, skip{it.Path, err.Error()})
				continue
			}
			done.Accepted = append(done.Accepted, it.Path)
		}
		return nil
	})
	if err != nil {
		done.Skipped = append(done.Skipped, skip{"", err.Error()})
	}
	if len(done.Accepted) > 0 {
		a.relinkHere()
	}
	return done
}

func (a *app) printDone(prefix string, d acceptDone) {
	if len(d.Accepted) > 0 {
		fmt.Fprintf(a.out, "%saccepted %s\n", prefix, plural(len(d.Accepted), "change"))
	}
	for _, sk := range d.Skipped {
		fmt.Fprintf(a.out, "%sleft %s waiting: %s\n", prefix, agents.Printable(sk.Path), agents.Printable(sk.Why))
	}
	if codexHooks(d.Accepted) {
		fmt.Fprintf(a.out, "%s%s\n", prefix, codexTrustNote)
	}
}

// codexTrustNote: codex asks again before it runs a hook that changed.
const codexTrustNote = "Codex asks you to trust changed hooks again: open Codex and run /hooks to review and trust them"

func codexHooks(paths []string) bool {
	for _, p := range paths {
		if strings.HasPrefix(p, "files/codex/") || (strings.HasPrefix(p, "machines/") && strings.Contains(p, "/files/codex/")) {
			return true
		}
	}
	return false
}

// printSummary groups each machine's items by kind: path, origin, age, size.
func printSummary(w io.Writer, ls []acceptList, headers bool) {
	for _, l := range ls {
		if headers {
			switch {
			case l.Error != "":
				fmt.Fprintf(w, "\n%s: not read (%s)\n", l.Machine, agents.Printable(l.Error))
				continue
			case len(l.Items) == 0:
				fmt.Fprintf(w, "\n%s: nothing waits\n", l.Machine)
				continue
			default:
				fmt.Fprintf(w, "\n%s: %s\n", l.Machine, plural(len(l.Items), "change"))
			}
		}
		width := 0
		for _, it := range l.Items {
			width = max(width, len(agents.Printable(it.Path)))
		}
		width = min(width, 48)
		for _, k := range kindOrder {
			var in []acceptItem
			for _, it := range l.Items {
				if it.Kind == k {
					in = append(in, it)
				}
			}
			if len(in) == 0 {
				continue
			}
			fmt.Fprintf(w, "  %s (%d)\n", k, len(in))
			for _, it := range in {
				fmt.Fprintf(w, "    %-*s  %-34s  %s\n", width, agents.Printable(it.Path), originText(it.Origin), changeText(it))
			}
		}
	}
}

func changeText(it acceptItem) string {
	var parts []string
	if it.New {
		parts = append(parts, fmt.Sprintf("new, %s, %s", plural(it.Lines, "line"), humanBytes(it.Bytes)))
	} else {
		parts = append(parts, fmt.Sprintf("+%d -%d lines", it.Added, it.Removed))
	}
	if it.Mode != "" && !it.New {
		parts = append(parts, strings.TrimPrefix(it.Mode, "mode: "))
	}
	return strings.Join(parts, ", ")
}

func diffsText(ls []acceptList) string {
	var b strings.Builder
	for _, l := range ls {
		for _, it := range l.Items {
			fmt.Fprintf(&b, "=== %s: %s (%s)\n", l.Machine, agents.Printable(it.Path), originText(it.Origin))
			b.WriteString(it.Diff)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// acceptPaths asks about each named path, one at a time: the old way.
func (a *app) acceptPaths(args []string) error {
	s, _, err := a.agentStore("agents accept")
	if err != nil {
		return err
	}
	items, err := listPending(s)
	if err != nil {
		return err
	}
	items = slices.DeleteFunc(items, func(it acceptItem) bool {
		return !slices.Contains(args, it.Path) && !slices.Contains(args, storeRel(s, it.Path))
	})
	if len(items) == 0 {
		fmt.Fprintln(a.out, "nothing waits for review at those paths")
		return nil
	}
	if !acceptTTY() {
		for _, it := range items {
			fmt.Fprintf(a.out, "waiting: %s (%s)\n", agents.Printable(it.Path), originText(it.Origin))
		}
		return notTerminal(false)
	}
	var yes []acceptItem
	for _, it := range items {
		fmt.Fprintf(a.out, "\n%s (%s)\n", agents.Printable(it.Path), originText(it.Origin))
		fmt.Fprint(a.out, it.Diff)
		if !acceptAsk("Make " + agents.Printable(it.Path) + " live on this machine?") {
			fmt.Fprintf(a.out, "left %s waiting\n", agents.Printable(it.Path))
			continue
		}
		yes = append(yes, it)
	}
	if len(yes) > 0 {
		done := a.acceptHere(s, yes)
		for _, p := range done.Accepted {
			fmt.Fprintf(a.out, "accepted %s\n", agents.Printable(p))
		}
		for _, sk := range done.Skipped {
			fmt.Fprintf(a.out, "left %s waiting: %s\n", agents.Printable(sk.Path), sk.Why)
		}
		if codexHooks(done.Accepted) {
			fmt.Fprintln(a.out, codexTrustNote)
		}
	}
	return nil
}

// acceptFleet is accept --all-machines: one summary of every online machine,
// one confirmation at this terminal, then each machine accepts what it listed.
func (a *app) acceptFleet(show bool) error {
	self, err := a.selfName()
	if err != nil {
		return err
	}
	if !a.store.LayerOn(registry.LayerAgents) {
		return layerOff(registry.LayerAgents, "agents accept")
	}
	// the person is checked first: nothing is asked of any machine without one
	if !acceptTTY() {
		return notTerminal(true)
	}
	ms, err := a.store.Machines()
	if err != nil {
		return err
	}
	var lists []acceptList
	for _, m := range ms {
		if m.Name == self {
			s, err := store.Open(agents.StoreName, self)
			if err != nil {
				return err
			}
			items, err := listPending(s)
			l := acceptList{Machine: self + " (this machine)", Items: items, self: true}
			if err != nil {
				l.Error = err.Error()
			}
			lists = append(lists, l)
			continue
		}
		lists = append(lists, a.listOn(m, self))
	}
	total, machines := 0, 0
	for _, l := range lists {
		total += len(l.Items)
		if len(l.Items) > 0 {
			machines++
		}
	}
	fmt.Fprintf(a.out, "%s wait%s for review on %s:\n", plural(total, "change"), verbS(total), plural(machines, "machine"))
	printSummary(a.out, lists, true)
	if total == 0 {
		return nil
	}
	if show {
		acceptPage(a.out, diffsText(lists))
	}
	for {
		q := fmt.Sprintf("Accept all %s on %s?", plural(total, "change"), plural(machines, "machine"))
		switch acceptChoose(q, []string{choiceAccept, choiceShow, choiceNo}) {
		case choiceAccept:
			return a.applyFleet(self, ms, lists)
		case choiceShow:
			acceptPage(a.out, diffsText(lists))
		default:
			fmt.Fprintln(a.out, "nothing accepted; it all still waits")
			return nil
		}
	}
}

// listOn asks m what waits there, through _exec.
func (a *app) listOn(m registry.Machine, self string) acceptList {
	l := acceptList{Machine: m.Name}
	var out, errb bytes.Buffer
	code, err := a.execOn(m, self, yonderCmd(m, "_accept-list"), nil, &out, &errb)
	switch {
	case err != nil:
		l.Error = firstLine(err.Error())
		return l
	case code != 0 && strings.Contains(errb.String(), "unknown command"):
		l.Error = "yonder there is older than 0.4; upgrade it there, or run `yonder agents accept` on it"
		return l
	case code != 0:
		l.Error = "exit " + fmt.Sprint(code) + ": " + firstLine(errb.String())
		return l
	}
	var got acceptList
	if err := json.Unmarshal(lastJSON(out.Bytes()), &got); err != nil {
		l.Error = "bad reply: " + err.Error()
		return l
	}
	// a reply speaks for that machine only, whatever it calls itself
	l.Ticket, l.Items = got.Ticket, got.Items
	if l.Items == nil {
		l.Items = []acceptItem{}
	}
	return l
}

// lastJSON is the last line of out: a login shell's profile may print first.
func lastJSON(out []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	return lines[len(lines)-1]
}

func (a *app) applyFleet(self string, ms []registry.Machine, lists []acceptList) error {
	byName := map[string]registry.Machine{}
	for _, m := range ms {
		byName[m.Name] = m
	}
	failed := false
	for _, l := range lists {
		if len(l.Items) == 0 || l.Error != "" {
			continue
		}
		if l.self {
			s, err := store.Open(agents.StoreName, self)
			if err != nil {
				return err
			}
			a.printDone(self+": ", a.acceptHere(s, l.Items))
			continue
		}
		want := acceptWant{Ticket: l.Ticket}
		for _, it := range l.Items {
			want.Items = append(want.Items, wantItem{it.Path, it.Hash, it.Exec})
		}
		body, _ := json.Marshal(want)
		m := byName[l.Machine]
		var out, errb bytes.Buffer
		code, err := a.execOn(m, self, yonderCmd(m, "_accept-apply"), bytes.NewReader(body), &out, &errb)
		if err != nil || code != 0 {
			why := firstLine(errb.String())
			if err != nil {
				why = err.Error()
			}
			fmt.Fprintf(a.out, "%s: nothing accepted: %s\n", l.Machine, agents.Printable(why))
			failed = true
			continue
		}
		var d acceptDone
		if err := json.Unmarshal(lastJSON(out.Bytes()), &d); err != nil {
			fmt.Fprintf(a.out, "%s: bad reply: %v\n", l.Machine, err)
			failed = true
			continue
		}
		a.printDone(l.Machine+": ", d)
	}
	if failed {
		return quietExit(ExitError)
	}
	return nil
}

// tickets live beside the store's state: id -> what was listed, for whom
type ticket struct {
	From    string     `json:"from"`
	Created time.Time  `json:"created"`
	Items   []wantItem `json:"items"`
}

func ticketsPath() string { return filepath.Join(registry.StateDir(), "sync", "accept-tickets.json") }

func loadTickets() map[string]ticket {
	out := map[string]ticket{}
	if data, err := os.ReadFile(ticketsPath()); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	for id, t := range out {
		if time.Since(t.Created) > ticketTTL || time.Until(t.Created) > time.Minute {
			delete(out, id)
		}
	}
	return out
}

func saveTickets(ts map[string]ticket) error {
	data, _ := json.Marshal(ts)
	if err := os.MkdirAll(filepath.Dir(ticketsPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(ticketsPath(), data, 0o600)
}

// caller names the fleet machine running this through _exec over ssh, or ""
// for this machine itself.
func (a *app) caller() (string, error) {
	conn := os.Getenv("SSH_CONNECTION")
	if conn == "" {
		return "", nil
	}
	who, err := remote.Sender(a.ctx, a.store, conn)
	if err != nil {
		return "", err
	}
	if who == "" {
		return "", errors.New("this machine has no registry yet")
	}
	return who, nil
}

// newAcceptListCmd is _accept-list: what waits here, as JSON, with a ticket
// that lets the machine that asked accept exactly that, once, for 10 minutes.
func newAcceptListCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_accept-list",
		Hidden: true,
		Args:   usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			s, self, err := a.agentStore("agents accept")
			if err != nil {
				return err
			}
			from, err := a.caller()
			if err != nil {
				return err
			}
			items, err := listPending(s)
			if err != nil {
				return err
			}
			l := acceptList{Machine: self, Items: items}
			if len(items) > 0 {
				buf := make([]byte, 16)
				if _, err := rand.Read(buf); err != nil {
					return err
				}
				l.Ticket = hex.EncodeToString(buf)
				t := ticket{From: from, Created: time.Now().UTC()}
				for _, it := range items {
					t.Items = append(t.Items, wantItem{it.Path, it.Hash, it.Exec})
				}
				if err := s.Lock(30 * time.Second); err != nil {
					return err
				}
				ts := loadTickets()
				ts[l.Ticket] = t
				err := saveTickets(ts)
				s.Unlock()
				if err != nil {
					return err
				}
			}
			data, err := json.Marshal(l)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.out, string(data))
			return nil
		},
	}
}

// newAcceptApplyCmd is _accept-apply: accept what a ticket listed. the
// ticket must be one this machine gave the same caller in the last 10
// minutes, and each item must still be the exact content listed.
func newAcceptApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_accept-apply",
		Hidden: true,
		Args:   usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			s, _, err := a.agentStore("agents accept")
			if err != nil {
				return err
			}
			var want acceptWant
			if err := json.NewDecoder(io.LimitReader(a.in, 4<<20)).Decode(&want); err != nil {
				return fmt.Errorf("bad request: %w", err)
			}
			from, err := a.caller()
			if err != nil {
				return err
			}
			if err := s.Lock(30 * time.Second); err != nil {
				return err
			}
			ts := loadTickets()
			t, ok := ts[want.Ticket]
			delete(ts, want.Ticket) // once
			err = saveTickets(ts)
			s.Unlock()
			if err != nil {
				return err
			}
			if !ok || want.Ticket == "" {
				return errors.New("no such ticket here, or it expired (they last 10 minutes); run `yonder agents accept --all-machines` again")
			}
			if t.From != from {
				return fmt.Errorf("this ticket was given to %s, not %s", orStr(t.From, "this machine"), orStr(from, "this machine"))
			}
			var items []acceptItem
			for _, w := range want.Items {
				if slices.Contains(t.Items, w) {
					items = append(items, acceptItem{Path: w.Path, Hash: w.Hash, Exec: w.Exec})
				}
			}
			done := a.acceptHere(s, items)
			for _, w := range want.Items {
				if !slices.Contains(t.Items, w) {
					done.Skipped = append(done.Skipped, skip{w.Path, "not in the list this machine gave"})
				}
			}
			data, _ := json.Marshal(done)
			fmt.Fprintln(a.out, string(data))
			return nil
		},
	}
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// acceptHint is the line that names the command for what waits.
func acceptHint(here, elsewhere int) string {
	switch {
	case here > 0 && elsewhere > 0:
		return fmt.Sprintf("%s wait for review here and on other machines; to review them all from this terminal: yonder agents accept --all-machines", plural(here+elsewhere, "change"))
	case here > 0:
		return fmt.Sprintf("%s wait%s for review here: yonder agents accept --all (in a terminal)", plural(here, "change"), verbS(here))
	case elsewhere > 0:
		return fmt.Sprintf("%s wait%s for review on other machines; to review them from this terminal: yonder agents accept --all-machines", plural(elsewhere, "change"), verbS(elsewhere))
	}
	return ""
}
