package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/zacharytgray/yonder/internal/discover"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/runlog"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

// Commands are the hidden subcommands a caller runs on a target.
var Commands = map[string]transport.Handler{
	"_exec":      Exec,
	"_job":       Job,
	"_jobs":      Jobs,
	"_sync":      Sync,
	"_log":       Log,
	"_facts":     FactsCmd,
	"_init-self": InitSelf,
}

// Dispatch runs args[0] as a hidden command. Local uses it to skip ssh.
func Dispatch(ctx context.Context, args []string, in io.Reader, out, errw io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errw, "yonder: no hidden command")
		return 2
	}
	h, ok := Commands[args[0]]
	if !ok {
		fmt.Fprintf(errw, "yonder: unknown hidden command %s\n", args[0])
		return 2
	}
	if in == nil {
		in = strings.NewReader("")
	}
	return h(ctx, args[1:], in, out, errw)
}

// Sync is `yonder _sync`: bundle JSON on stdin, merged bundle JSON on stdout.
// under sshd it merges only from a known fleet machine, under FromPeer's rules.
// it also strips lost machines' keys from authorized_keys here, once each.
func Sync(ctx context.Context, _ []string, in io.Reader, out, errw io.Writer) int {
	var b registry.Bundle
	if err := json.NewDecoder(io.LimitReader(in, 16<<20)).Decode(&b); err != nil {
		fmt.Fprintf(errw, "yonder _sync: %v\n", err)
		return 1
	}
	s := registry.Open()
	var merged registry.Bundle
	var err error
	if conn := os.Getenv("SSH_CONNECTION"); conn == "" {
		merged, err = s.MergeIn(b) // our own cli, in process
	} else {
		var sender string
		if sender, err = Sender(ctx, s, conn); err != nil {
			fmt.Fprintf(errw, "yonder _sync: refused: %v\n", err)
			return 1
		}
		merged, err = s.MergeFrom(b, sender)
	}
	if err != nil {
		fmt.Fprintf(errw, "yonder _sync: %v\n", err)
		return 1
	}
	if _, err := StripLost(merged.Tombstones, merged.Machines); err != nil {
		fmt.Fprintf(errw, "yonder _sync: %v\n", err) // doctor flags keys left behind
	}
	if err := json.NewEncoder(out).Encode(merged); err != nil {
		return 1
	}
	return 0
}

// Sender names the fleet machine behind SSH_CONNECTION: its source address
// is a tailnet peer, and a record here has that tailscale_ip. a machine with
// an empty registry (just added) takes its first sync from anyone who got in.
func Sender(ctx context.Context, s *registry.Store, conn string) (string, error) {
	f := strings.Fields(conn)
	if len(f) == 0 {
		return "", fmt.Errorf("bad SSH_CONNECTION %q", conn)
	}
	ip := f[0]
	local, err := s.Bundle()
	if err != nil {
		return "", err
	}
	if len(local.Machines) == 0 && len(local.Tombstones) == 0 {
		return "", nil
	}
	st, err := tailnet.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("can't check who %s is: %v", ip, err)
	}
	p, ok := st.LookupIP(ip)
	if !ok {
		return "", fmt.Errorf("%s is not a peer in this tailnet", ip)
	}
	if _, ok := s.Tombstone(p.Name()); ok {
		return "", fmt.Errorf("%s (%s) was removed from the fleet", p.Name(), ip)
	}
	sender := ""
	for _, m := range local.Machines {
		if m.TailscaleIP != "" && slices.Contains(p.TailscaleIPs, m.TailscaleIP) && (sender == "" || m.Name == p.Name()) {
			sender = m.Name
		}
	}
	if sender != "" {
		return sender, nil
	}
	return "", fmt.Errorf("%s (%s) is not in this machine's registry", p.Name(), ip)
}

// Log is `yonder _log [--since RFC3339]`: this machine's log as JSONL.
func Log(_ context.Context, args []string, _ io.Reader, out, errw io.Writer) int {
	var since time.Time
	for i := 0; i < len(args); i++ {
		a := args[i]
		v, ok := strings.CutPrefix(a, "--since=")
		if !ok && a == "--since" && i+1 < len(args) {
			i++
			v, ok = args[i], true
		}
		if !ok {
			fmt.Fprintf(errw, "yonder _log: unknown argument %s\n", a)
			return 2
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			fmt.Fprintf(errw, "yonder _log: --since: %v\n", err)
			return 2
		}
		since = t
	}
	es, err := runlog.Read(registry.LogPath(), since)
	if err != nil {
		fmt.Fprintf(errw, "yonder _log: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(out)
	for _, e := range es {
		if err := enc.Encode(e); err != nil {
			return 1
		}
	}
	return 0
}

// FactsCmd is `yonder _facts`.
func FactsCmd(_ context.Context, _ []string, _ io.Reader, out, _ io.Writer) int {
	if err := json.NewEncoder(out).Encode(discover.Local()); err != nil {
		return 1
	}
	return 0
}

// InitSelf is `yonder _init-self <name>`: tells a machine its fleet name.
func InitSelf(_ context.Context, args []string, _ io.Reader, _, errw io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(errw, "usage: yonder _init-self <name>")
		return 2
	}
	if err := registry.Open().SetSelf(args[0]); err != nil {
		fmt.Fprintf(errw, "yonder _init-self: %v\n", err)
		return 1
	}
	return 0
}

// Jobs is `yonder _jobs [id]`: a JSON list of jobs, or one job's raw log.
func Jobs(_ context.Context, args []string, _ io.Reader, out, errw io.Writer) int {
	dir := registry.JobsDir()
	if len(args) == 1 {
		id := args[0]
		if !ValidID(id) {
			fmt.Fprintf(errw, "yonder: bad job id %q\n", id)
			return 2
		}
		f, err := os.Open(filepath.Join(dir, id+".log"))
		if err != nil {
			fmt.Fprintf(errw, "yonder: no job %s\n", id)
			return 1
		}
		defer f.Close()
		if _, err := io.Copy(out, f); err != nil {
			return 1
		}
		return 0
	}
	if len(args) > 1 {
		fmt.Fprintln(errw, "usage: yonder _jobs [id]")
		return 2
	}
	list, err := ListJobs(dir)
	if err != nil {
		fmt.Fprintf(errw, "yonder: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(out).Encode(list); err != nil {
		return 1
	}
	return 0
}

// ListJobs reads every jobs/*.json, newest first. a running job whose pid is gone is "lost".
func ListJobs(dir string) ([]JobStatus, error) {
	out := []JobStatus{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var st JobStatus
		if json.Unmarshal(data, &st) != nil {
			continue
		}
		if st.State == "running" && st.PID > 0 && errors.Is(syscall.Kill(st.PID, 0), syscall.ESRCH) {
			st.State = "lost"
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}
