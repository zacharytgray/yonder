// Package schedule installs the timer that runs `yonder sync` every 15
// minutes: a launchd LaunchAgent on macOS, a systemd user timer on Linux.
// it is a timer, not a daemon: nothing stays resident between runs.
package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
)

// the names an existing hand-made job may already use; yonder takes those over
const (
	Label = "com.yonder.sync" // launchd
	Unit  = "yonder-sync"     // systemd: yonder-sync.service and yonder-sync.timer
)

// Marker is in every file yonder writes, so a job someone made by hand is told apart.
const Marker = "yonder:schedule v1"

// Interval is how often the job runs.
const Interval = 15 * time.Minute

// Job is what the timer runs, on one machine.
type Job struct {
	OS       string            // darwin | linux
	Bin      string            // the yonder binary, at a path that survives upgrades
	Self     string            // this machine's fleet name: spreads machines over the interval
	Home     string            // ~
	StateDir string            // yonder's state dir; the log goes in sync/
	Env      map[string]string // carried into the job (XDG dirs, YONDER_HOME)
}

// Offset is the minute in each 15 this machine syncs at, so a fleet's
// machines don't all sync at once and find each other busy.
func Offset(self string) int {
	h := fnv.New32a()
	h.Write([]byte(self))
	return int(h.Sum32() % 15)
}

// LogPath is where each run's output goes.
func LogPath(stateDir string) string { return filepath.Join(stateDir, "sync", "schedule.log") }

// runPath holds the last run: start, end, exit.
func runPath(stateDir string) string { return filepath.Join(stateDir, "sync", "schedule.json") }

// declinedPath marks a machine where the person said no schedule.
func declinedPath(stateDir string) string { return filepath.Join(stateDir, "schedule", "declined") }

func backupDir(stateDir string) string {
	return filepath.Join(stateDir, "schedule", "backup", time.Now().UTC().Format("20060102-150405"))
}

// Files are the paths the job lives at, for this OS.
func Files(goos, home string) []string {
	if goos == "darwin" {
		return []string{filepath.Join(home, "Library", "LaunchAgents", Label+".plist")}
	}
	d := filepath.Join(home, ".config", "systemd", "user")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && os.Getenv("YONDER_HOME") == "" {
		d = filepath.Join(x, "systemd", "user")
	}
	return []string{filepath.Join(d, Unit+".service"), filepath.Join(d, Unit+".timer")}
}

// path is the job's PATH: the binary's own folder first, then the usual ones,
// since launchd and systemd start jobs with a bare PATH.
func (j Job) path() string {
	dirs := []string{filepath.Dir(j.Bin), filepath.Join(j.Home, ".local", "bin")}
	if j.OS == "darwin" {
		dirs = append(dirs, "/opt/homebrew/bin")
	} else {
		dirs = append(dirs, "/home/linuxbrew/.linuxbrew/bin")
	}
	dirs = append(dirs, "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, ":")
}

func (j Job) env() map[string]string {
	e := map[string]string{"PATH": j.path()}
	if j.OS == "darwin" {
		e["SHLVL"] = "1" // the tailscale app binary acts as its cli only when SHLVL is set
	}
	for k, v := range j.Env {
		e[k] = v
	}
	return e
}

// EnvFromProcess is what a job must keep from yonder's own environment:
// anything that moves its config, state or stores.
func EnvFromProcess() map[string]string {
	out := map[string]string{}
	for _, k := range []string{"YONDER_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "YONDER_SSH_IDENTITY", "YONDER_TAILSCALE_JSON"} {
		if v := os.Getenv(k); v != "" {
			if k == "YONDER_HOME" {
				if abs, err := filepath.Abs(v); err == nil {
					v = abs
				}
			}
			out[k] = v
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// Plist is the LaunchAgent. StartCalendarInterval, not StartInterval: launchd
// runs a calendar job missed during sleep once on wake, and skips a missed
// StartInterval. RunAtLoad runs it at login.
func (j Job) Plist() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- ` + Marker + `: written by yonder; ` + "`yonder schedule remove`" + ` takes it out -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(j.Bin) + `</string>
		<string>sync</string>
		<string>--scheduled</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
`)
	env := j.env()
	for _, k := range sortedKeys(env) {
		b.WriteString("\t\t<key>" + xmlEscape(k) + "</key>\n\t\t<string>" + xmlEscape(env[k]) + "</string>\n")
	}
	b.WriteString(`	</dict>
	<key>WorkingDirectory</key>
	<string>` + xmlEscape(j.Home) + `</string>
	<key>StartCalendarInterval</key>
	<array>
`)
	off := Offset(j.Self)
	for m := off; m < 60; m += 15 {
		b.WriteString("\t\t<dict><key>Minute</key><integer>" + strconv.Itoa(m) + "</integer></dict>\n")
	}
	log := xmlEscape(LogPath(j.StateDir))
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + log + `</string>
	<key>StandardErrorPath</key>
	<string>` + log + `</string>
</dict>
</plist>
`)
	return b.String()
}

// systemd quoting: a word in double quotes with \ and " escaped
func sdQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Service is the oneshot unit the timer starts.
func (j Job) Service() string {
	var b strings.Builder
	b.WriteString("# " + Marker + ": written by yonder; `yonder schedule remove` takes it out\n")
	b.WriteString("[Unit]\nDescription=yonder sync: the registry and the agents store with every online machine\n\n[Service]\nType=oneshot\n")
	b.WriteString("ExecStart=" + sdQuote(j.Bin) + " sync --scheduled\n")
	b.WriteString("WorkingDirectory=" + j.Home + "\n")
	env := j.env()
	for _, k := range sortedKeys(env) {
		b.WriteString("Environment=" + sdQuote(k+"="+env[k]) + "\n")
	}
	log := LogPath(j.StateDir)
	b.WriteString("StandardOutput=append:" + log + "\nStandardError=append:" + log + "\n")
	return b.String()
}

// Timer runs the service every 15 minutes on the wall clock (a calendar timer
// whose time passed during suspend fires on resume), catches up a run missed
// while off (Persistent), and runs once a minute after the user manager
// starts, which is login.
func (j Job) Timer() string {
	off := Offset(j.Self)
	return "# " + Marker + ": written by yonder; `yonder schedule remove` takes it out\n" +
		"[Unit]\nDescription=run yonder sync every 15 minutes\n\n[Timer]\n" +
		fmt.Sprintf("OnCalendar=*:%02d/15\n", off) +
		"OnStartupSec=1min\nPersistent=true\nAccuracySec=30s\n\n[Install]\nWantedBy=timers.target\n"
}

// contents is each file and what goes in it, for this job's OS.
func (j Job) contents() map[string]string {
	fs := Files(j.OS, j.Home)
	if j.OS == "darwin" {
		return map[string]string{fs[0]: j.Plist()}
	}
	return map[string]string{fs[0]: j.Service(), fs[1]: j.Timer()}
}

// ExecLine is the command a job file runs, read back from it: what a test
// runs to trigger the schedule directly.
func ExecLine(goos, home string) ([]string, error) {
	fs := Files(goos, home)
	data, err := os.ReadFile(fs[0])
	if err != nil {
		return nil, err
	}
	if goos == "darwin" {
		s := string(data)
		i := strings.Index(s, "<key>ProgramArguments</key>")
		if i < 0 {
			return nil, errors.New("no ProgramArguments")
		}
		s = s[i:]
		end := strings.Index(s, "</array>")
		var out []string
		for _, part := range strings.Split(s[:end], "<string>")[1:] {
			v, _, _ := strings.Cut(part, "</string>")
			out = append(out, strings.NewReplacer("&quot;", `"`, "&apos;", "'", "&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(v))
		}
		return out, nil
	}
	for _, l := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(l, "ExecStart="); ok {
			bin, args, _ := strings.Cut(rest, `" `)
			bin = strings.TrimPrefix(bin, `"`)
			return append([]string{strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(bin)}, strings.Fields(args)...), nil
		}
	}
	return nil, errors.New("no ExecStart")
}

// Run is one run of the scheduled sync, as it recorded itself.
type Run struct {
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished"`
	Exit     *int       `json:"exit"`
	Error    string     `json:"error,omitempty"`
}

// RecordStart notes a scheduled run starting, and trims a log grown past 1 MB.
func RecordStart(stateDir string) {
	if st, err := os.Stat(LogPath(stateDir)); err == nil && st.Size() > 1<<20 {
		_ = os.Truncate(LogPath(stateDir), 0)
	}
	writeRun(stateDir, Run{Started: time.Now().UTC()})
}

// RecordEnd notes how the run ended.
func RecordEnd(stateDir string, code int, err error) {
	r, _ := LastRun(stateDir)
	if r == nil {
		r = &Run{Started: time.Now().UTC()}
	}
	now := time.Now().UTC()
	r.Finished, r.Exit = &now, &code
	if err != nil {
		r.Error = err.Error()
	}
	writeRun(stateDir, *r)
}

func writeRun(stateDir string, r Run) {
	data, _ := json.Marshal(r)
	p := runPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o600)
}

// LastRun is the last scheduled run here, or nil.
func LastRun(stateDir string) (*Run, error) {
	data, err := os.ReadFile(runPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Declined says the person turned the schedule off on this machine.
func Declined(stateDir string) bool {
	_, err := os.Stat(declinedPath(stateDir))
	return err == nil
}

// manage: whether to call launchctl or systemctl. never under YONDER_HOME,
// so tests write the files and leave the real service manager alone.
func manage() bool { return os.Getenv("YONDER_HOME") == "" }

// noManager: linux without systemctl (a container, a minimal install). the
// timer can't run there; other machines' syncs still reach it. a var for tests.
var noManager = func(goos string) bool {
	if goos == "darwin" {
		return false
	}
	_, err := exec.LookPath("systemctl")
	return err != nil
}

// command runs a service-manager command. a var for tests.
var command = func(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func uid() string { return strconv.Itoa(os.Getuid()) }

// Result is what Install or Remove did, in a line each.
type Result struct {
	Lines []string
	Warn  []string // done, but something needs the person (no gui session, no user manager)
}

// Install writes the job and loads it. a job with the same label that yonder
// didn't write (made by hand) is backed up and replaced, never run alongside.
// it clears a decline.
func Install(j Job) (Result, error) {
	var res Result
	if j.Bin == "" || !filepath.IsAbs(j.Bin) {
		return res, fmt.Errorf("the yonder binary path %q is not absolute", j.Bin)
	}
	_ = os.Remove(declinedPath(j.StateDir))
	if err := os.MkdirAll(filepath.Join(j.StateDir, "sync"), 0o700); err != nil {
		return res, err
	}
	if j.current() {
		return res, nil
	}
	var bdir string
	backup := func(p string) error {
		if bdir == "" {
			bdir = backupDir(j.StateDir)
			if err := os.MkdirAll(bdir, 0o700); err != nil {
				return err
			}
		}
		return os.Rename(p, filepath.Join(bdir, filepath.Base(p)))
	}
	// unload first: a running definition with this label, ours or not
	if manage() {
		res.Lines = append(res.Lines, unload(j)...)
		if j.OS == "darwin" {
			if other := loadedPath(); other != "" && other != Files(j.OS, j.Home)[0] && strings.HasPrefix(other, j.Home+"/") {
				if _, err := os.Stat(other); err == nil {
					if err := backup(other); err != nil {
						return res, err
					}
					res.Lines = append(res.Lines, "moved another "+Label+" job ("+other+") to "+bdir)
				}
			}
		}
	}
	for p, content := range j.contents() {
		if old, err := os.ReadFile(p); err == nil && !strings.Contains(string(old), Marker) {
			if err := backup(p); err != nil {
				return res, err
			}
			res.Lines = append(res.Lines, "took over the existing "+filepath.Base(p)+" that yonder didn't write; it is backed up in "+bdir)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(p+".yonder-tmp", []byte(content), 0o644); err != nil {
			return res, err
		}
		if err := os.Rename(p+".yonder-tmp", p); err != nil {
			return res, err
		}
	}
	if !manage() {
		res.Lines = append(res.Lines, "wrote "+strings.Join(Files(j.OS, j.Home), ", ")+" (not loaded: YONDER_HOME is set)")
		return res, nil
	}
	w := load(j)
	res.Warn = append(res.Warn, w...)
	if noManager(j.OS) {
		return res, nil // the timer can't run here; the warning says so
	}
	res.Lines = append(res.Lines, fmt.Sprintf("yonder sync runs every 15 minutes here (at :%02d and every 15 after), at login and on wake; log %s", Offset(j.Self), LogPath(j.StateDir)))
	return res, nil
}

// current: every file holds exactly what Install would write, and the
// service manager has it loaded.
func (j Job) current() bool {
	for p, content := range j.contents() {
		if old, err := os.ReadFile(p); err != nil || string(old) != content {
			return false
		}
	}
	if !manage() || noManager(j.OS) {
		return true
	}
	if j.OS == "darwin" {
		return loadedPath() == Files(j.OS, j.Home)[0]
	}
	out, err := command("systemctl", "--user", "is-active", Unit+".timer")
	return err == nil && strings.TrimSpace(out) == "active"
}

func unload(j Job) []string {
	if noManager(j.OS) {
		return nil
	}
	if j.OS == "darwin" {
		if _, err := command("launchctl", "print", "gui/"+uid()+"/"+Label); err == nil {
			_, _ = command("launchctl", "bootout", "gui/"+uid()+"/"+Label)
		}
		return nil
	}
	_, _ = command("systemctl", "--user", "disable", "--now", Unit+".timer")
	return nil
}

// loadedPath is the plist launchd loaded for the label, or "".
func loadedPath() string {
	out, err := command("launchctl", "print", "gui/"+uid()+"/"+Label)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "path = "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func load(j Job) []string {
	if j.OS == "darwin" {
		out, err := command("launchctl", "bootstrap", "gui/"+uid(), Files(j.OS, j.Home)[0])
		if err != nil {
			return []string{"launchd didn't load it now (" + firstLine(out, err) + "); macOS loads it at your next login. With nobody logged in to this Mac's desktop, launchd has no session to run it in"}
		}
		return nil
	}
	if noManager(j.OS) {
		return []string{"this machine has no systemd, so the 15-minute timer can't run here. It still gets every change when another machine syncs with it; `yonder schedule remove` stops this note"}
	}
	if out, err := command("systemctl", "--user", "daemon-reload"); err != nil {
		return []string{"systemd didn't load it (" + firstLine(out, err) + "); the systemd user manager isn't running for this user. Log in once, or run `sudo loginctl enable-linger " + userName() + "` so it runs without a login, then `yonder schedule install`"}
	}
	if out, err := command("systemctl", "--user", "enable", "--now", Unit+".timer"); err != nil {
		return []string{"systemd didn't start the timer (" + firstLine(out, err) + "); run `systemctl --user enable --now " + Unit + ".timer`"}
	}
	if l := linger(); l != nil && !*l {
		return []string{"the timer runs only while you're logged in to this machine; for an always-on machine run `sudo loginctl enable-linger " + userName() + "`"}
	}
	return nil
}

func firstLine(out string, err error) string {
	if out == "" {
		return err.Error()
	}
	l, _, _ := strings.Cut(out, "\n")
	return l
}

func userName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "$USER"
}

func linger() *bool {
	out, err := command("loginctl", "show-user", userName(), "-p", "Linger", "--value")
	if err != nil {
		return nil
	}
	v := strings.TrimSpace(out) == "yes"
	return &v
}

// Remove unloads and deletes the job if yonder wrote it. a hand-made job
// with the label is left alone and named. decline also records that the
// person doesn't want one here, so a sync won't put it back.
func Remove(goos, home, stateDir string, decline bool) (Result, error) {
	var res Result
	if decline {
		if err := os.MkdirAll(filepath.Dir(declinedPath(stateDir)), 0o700); err != nil {
			return res, err
		}
		if err := os.WriteFile(declinedPath(stateDir), []byte("the schedule is off on this machine; `yonder schedule install` turns it back on\n"), 0o600); err != nil {
			return res, err
		}
	}
	fs := Files(goos, home)
	var ours, foreign []string
	for _, p := range fs {
		data, err := os.ReadFile(p)
		switch {
		case err != nil:
		case strings.Contains(string(data), Marker):
			ours = append(ours, p)
		default:
			foreign = append(foreign, p)
		}
	}
	if len(foreign) > 0 {
		res.Warn = append(res.Warn, "left "+strings.Join(foreign, ", ")+" alone: yonder didn't write it")
	}
	if len(ours) == 0 {
		if len(foreign) == 0 {
			res.Lines = append(res.Lines, "no sync schedule here")
		}
		return res, nil
	}
	if manage() {
		unload(Job{OS: goos, Home: home})
	}
	for _, p := range ours {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
	}
	if manage() && goos != "darwin" && !noManager(goos) {
		_, _ = command("systemctl", "--user", "daemon-reload")
	}
	res.Lines = append(res.Lines, "removed the sync schedule here")
	return res, nil
}

// Status is the schedule on one machine, for doctor and `schedule status`.
type Status struct {
	OS        string   `json:"os"`
	Files     []string `json:"files"`
	Installed bool     `json:"installed"` // a job with yonder's label exists
	Ours      bool     `json:"ours"`      // yonder wrote it
	Bin       string   `json:"bin"`       // the yonder it runs
	BinOK     bool     `json:"bin_ok"`    // that file exists
	Loaded    *bool    `json:"loaded"`    // null when not checked
	LoadError string   `json:"load_error,omitempty"`
	LastExit  *int     `json:"last_exit,omitempty"`  // as the service manager saw it
	Linger    *bool    `json:"linger,omitempty"`     // linux: runs without a login
	NoManager bool     `json:"no_manager,omitempty"` // linux without systemd: the timer can't run
	Declined  bool     `json:"declined"`
	LastRun   *Run     `json:"last_run"`
	Log       string   `json:"log"`
}

// Check reads the schedule's state here. it changes nothing.
func Check(goos, home, stateDir string) Status {
	s := Status{OS: goos, Files: Files(goos, home), Declined: Declined(stateDir), Log: LogPath(stateDir)}
	s.LastRun, _ = LastRun(stateDir)
	for i, p := range s.Files {
		data, err := os.ReadFile(p)
		if err != nil {
			if i == 0 {
				return s
			}
			continue
		}
		s.Installed = true
		s.Ours = strings.Contains(string(data), Marker)
		if i == 0 && s.Ours {
			if args, err := ExecLine(goos, home); err == nil && len(args) > 0 {
				s.Bin = args[0]
				_, err := os.Stat(s.Bin)
				s.BinOK = err == nil
			}
		}
	}
	if !s.Installed || !manage() {
		return s
	}
	if noManager(goos) {
		s.NoManager = true
		return s
	}
	loaded := false
	if goos == "darwin" {
		out, err := command("launchctl", "print", "gui/"+uid()+"/"+Label)
		if err == nil {
			loaded = true
			for _, l := range strings.Split(out, "\n") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(l), "last exit code = "); ok {
					if n, err := strconv.Atoi(strings.Fields(v)[0]); err == nil {
						s.LastExit = &n
					}
				}
			}
		} else {
			s.LoadError = firstLine(out, err)
		}
	} else {
		out, err := command("systemctl", "--user", "is-active", Unit+".timer")
		loaded = err == nil && strings.TrimSpace(out) == "active"
		if !loaded {
			s.LoadError = firstLine(out, err)
		}
		if out, err := command("systemctl", "--user", "show", Unit+".service", "-p", "ExecMainStatus", "--value"); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil {
				s.LastExit = &n
			}
		}
		s.Linger = linger()
	}
	s.Loaded = &loaded
	return s
}

// Problem is what is wrong with a schedule, and the fix, or "" when fine.
// fix names the command to run on that machine.
func (s Status) Problem(now time.Time) (status, detail, fix string) {
	switch {
	case s.Declined && !s.Installed:
		return "pass", "off here: someone ran `yonder schedule remove` or --no-schedule", ""
	case !s.Installed:
		return "fail", "no sync schedule: edits reach this machine only when a sync runs", "yonder schedule install"
	case !s.Ours:
		return "warn", "a " + Label + " job that yonder didn't write (" + s.Files[0] + ")", "yonder schedule install  (backs it up and takes it over)"
	case s.NoManager:
		return "warn", "no systemd here, so the timer can't run; a sync from another machine that has the timer still reaches this one, and `yonder sync` here works by hand", "nothing needed; `yonder schedule remove` stops this warning"
	case !s.BinOK:
		return "fail", "the job runs " + s.Bin + ", which is gone (moved by an upgrade?)", "yonder schedule install"
	case s.Loaded != nil && !*s.Loaded:
		d := "the job file is there, but the service manager hasn't loaded it"
		if s.LoadError != "" {
			d += " (" + s.LoadError + ")"
		}
		return "fail", d, "yonder schedule install"
	}
	if s.Linger != nil && !*s.Linger {
		return "warn", "the timer runs only while you're logged in here", "sudo loginctl enable-linger $USER  (for an always-on machine)"
	}
	r := s.LastRun
	switch {
	case r == nil:
		return "pass", "installed; no run yet", ""
	case r.Exit != nil && *r.Exit != 0:
		d := fmt.Sprintf("last run %s exited %d", ago(r.Started, now), *r.Exit)
		if r.Error != "" {
			d += ": " + r.Error
		}
		return "warn", d, "read " + s.Log + " on that machine; `yonder sync` there shows the same"
	case r.Finished == nil && now.Sub(r.Started) > time.Hour:
		return "warn", "last run started " + ago(r.Started, now) + " and never finished", "read " + s.Log + " on that machine"
	case now.Sub(r.Started) > 2*time.Hour:
		return "warn", "last run " + ago(r.Started, now) + " (asleep or off since? it runs on wake)", "yonder schedule install  (reloads it)"
	}
	return "pass", "every 15 minutes; last run " + ago(r.Started, now) + ", ok", ""
}

func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// StableBin swaps a binary inside a versioned Homebrew folder for the link
// Homebrew keeps in its bin, so the job survives `brew upgrade`.
func StableBin(exe string) string {
	if !strings.Contains(exe, "/Cellar/") && !strings.Contains(exe, "/Caskroom/") {
		return exe
	}
	for _, prefix := range []string{"/opt/homebrew", "/usr/local", "/home/linuxbrew/.linuxbrew"} {
		if strings.HasPrefix(exe, prefix+"/") {
			p := prefix + "/bin/yonder"
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return exe
}

// SelfPath is this binary at the path a person would type: the PATH entry
// when it is this file, so a brew link stays a link.
func SelfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "yonder"
	}
	if lp, err := exec.LookPath("yonder"); err == nil {
		a, errA := os.Stat(lp)
		b, errB := os.Stat(exe)
		if errA == nil && errB == nil && os.SameFile(a, b) {
			if abs, err := filepath.Abs(lp); err == nil {
				return abs
			}
		}
	}
	return exe
}

// Here is the job for this machine.
func Here(self string) Job {
	return Job{OS: runtime.GOOS, Bin: StableBin(SelfPath()), Self: self, Home: registry.UserHome(),
		StateDir: registry.StateDir(), Env: EnvFromProcess()}
}

// Apply keeps the schedule in line with the agents layer here: off removes
// the one yonder wrote; on installs one where none is (unless declined) and
// repairs one whose binary moved. a hand-made job is left for an explicit
// `yonder schedule install`, which takes it over. it is cheap when nothing
// needs doing: it only reads the job file.
func Apply(on bool, self string) (Result, error) {
	j := Here(self)
	if !on {
		st := Check(j.OS, j.Home, j.StateDir)
		if !st.Installed || !st.Ours {
			return Result{}, nil
		}
		return Remove(j.OS, j.Home, j.StateDir, false)
	}
	if Declined(j.StateDir) {
		return Result{}, nil
	}
	fs := Files(j.OS, j.Home)
	data, err := os.ReadFile(fs[0])
	switch {
	case err == nil && !strings.Contains(string(data), Marker):
		return Result{}, nil
	case err == nil:
		if args, err := ExecLine(j.OS, j.Home); err == nil && len(args) > 0 {
			if _, err := os.Stat(args[0]); err == nil {
				return Result{}, nil
			}
		}
	}
	return Install(j)
}
