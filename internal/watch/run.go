package watch

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	frameEvery = 100 * time.Millisecond
	fetchEvery = 500 * time.Millisecond
)

// Run animates the view on a terminal until q, Ctrl-C or ctx ends it. It
// polls the service in the background and shows a fetch error in the footer
// while it keeps trying, so a restarted service picks up again.
func Run(ctx context.Context, in, out *os.File, state string, view *View) error {
	restore, err := MakeRaw(in)
	if err != nil {
		return err
	}
	defer restore()
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := in.Read(buf)
			if err != nil || (n == 1 && (buf[0] == 'q' || buf[0] == 3)) {
				cancel()
				return
			}
		}
	}()
	var mu sync.Mutex
	var fetchErr error
	go func() {
		t := time.NewTicker(fetchEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				snap, err := Fetch(state)
				mu.Lock()
				if err == nil {
					view.Update(snap, time.Now())
				}
				fetchErr = err
				mu.Unlock()
			}
		}
	}()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	cols, rows := size(out)
	frame := time.NewTicker(frameEvery)
	defer frame.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-winch:
			cols, rows = size(out)
		case <-frame.C:
			mu.Lock()
			lines := view.Render(time.Now(), cols, rows)
			err := fetchErr
			mu.Unlock()
			if err != nil {
				lines[len(lines)-1] = color(cFailed, fit(" "+err.Error()+" · retrying", cols))
			}
			var b strings.Builder
			b.WriteString("\x1b[H")
			for i, line := range lines {
				b.WriteString(line)
				b.WriteString("\x1b[K")
				if i < len(lines)-1 {
					b.WriteString("\r\n")
				}
			}
			_, _ = out.WriteString(b.String())
		}
	}
}

func size(out *os.File) (int, int) {
	if cols, rows, ok := Size(out); ok {
		return cols, rows
	}
	return 100, 30
}
