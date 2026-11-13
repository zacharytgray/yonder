package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/store"
)

// exits of the store commands that mean "skip this machine", not "failed"
const (
	ExitStoreOff  = 3 // the store's layer is off here
	ExitStoreBusy = 4 // another yonder holds the store's lock here
	ExitStoreHeld = 5 // a scan here would delete most of the store, so it stopped
)

func storeErr(name string, err error, errw io.Writer) int {
	fmt.Fprintf(errw, "yonder %s: %v\n", name, err)
	if errors.Is(err, store.ErrBusy) {
		return ExitStoreBusy
	}
	if errors.Is(err, store.ErrMassDelete) {
		return ExitStoreHeld
	}
	return 1
}

// openStore checks the caller and the layer, then opens the named store.
// under sshd only a fleet machine may sync, the same rule as _sync.
func openStore(ctx context.Context, name string, args []string, errw io.Writer) (*store.Store, string, int) {
	if len(args) != 1 {
		fmt.Fprintf(errw, "usage: yonder %s <store>\n", name)
		return nil, "", 2
	}
	k, ok := store.Lookup(args[0])
	if !ok {
		fmt.Fprintf(errw, "yonder %s: no store named %s\n", name, args[0])
		return nil, "", 2
	}
	reg := registry.Open()
	cfg, err := reg.Config()
	if err != nil || cfg.Self == "" {
		fmt.Fprintf(errw, "yonder %s: this machine is not set up\n", name)
		return nil, "", 1
	}
	sender := ""
	if conn := os.Getenv("SSH_CONNECTION"); conn != "" {
		if sender, err = Sender(ctx, reg, conn); err != nil {
			fmt.Fprintf(errw, "yonder %s: refused: %v\n", name, err)
			return nil, "", 1
		}
		if sender == "" {
			fmt.Fprintf(errw, "yonder %s: refused: this machine has no fleet registry yet\n", name)
			return nil, "", 1
		}
	}
	if !reg.LayerOn(k.Layer) {
		fmt.Fprintf(errw, "yonder %s: the %s layer is off on %s\n", name, k.Layer, cfg.Self)
		return nil, "", ExitStoreOff
	}
	s, err := store.Open(k.Name, cfg.Self)
	if err != nil {
		fmt.Fprintf(errw, "yonder %s: %v\n", name, err)
		return nil, "", 1
	}
	return s, sender, 0
}

// StoreOffer is `yonder _store-offer <store>`: scan, then print the manifest.
func StoreOffer(ctx context.Context, args []string, _ io.Reader, out, errw io.Writer) int {
	s, sender, code := openStore(ctx, "_store-offer", args, errw)
	if s == nil {
		return code
	}
	if err := store.ServeOffer(s, out, sender); err != nil {
		return storeErr("_store-offer", err, errw)
	}
	return 0
}

// StoreSend is `yonder _store-send <store>`: {"paths":[...]} on stdin, a bundle on stdout.
func StoreSend(ctx context.Context, args []string, in io.Reader, out, errw io.Writer) int {
	s, _, code := openStore(ctx, "_store-send", args, errw)
	if s == nil {
		return code
	}
	if err := store.ServeSend(s, in, out); err != nil {
		return storeErr("_store-send", err, errw)
	}
	return 0
}

// StoreRecv is `yonder _store-recv <store>`: a bundle on stdin, what it did on stdout.
func StoreRecv(ctx context.Context, args []string, in io.Reader, out, errw io.Writer) int {
	s, sender, code := openStore(ctx, "_store-recv", args, errw)
	if s == nil {
		return code
	}
	if err := store.ServeRecv(s, in, out, sender); err != nil {
		return storeErr("_store-recv", err, errw)
	}
	return 0
}
