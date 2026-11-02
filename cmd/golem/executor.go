// The execution endpoint owns commands and output, never agent sessions or model credentials.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golem/internal/executor"
)

func runExecutor(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("executor", flag.ContinueOnError)
	stdio := flags.Bool("stdio", false, "Serve JSONL over private stdin/stdout pipes")
	workspace := flags.String("workspace", ".", "Execution root")
	state := flags.String("state", ".golem-node", "Endpoint state directory")
	listen := flags.String("listen", "127.0.0.1:7373", "Authenticated HTTP listen address")
	retain := flags.Duration("retain", 336*time.Hour, "How long finished command output is kept")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *retain <= 0 {
		return errors.New("--retain must be a positive duration")
	}
	if !*stdio && os.Getenv("GOLEM_NODE_TOKEN") == "" {
		return errors.New("set GOLEM_NODE_TOKEN before starting HTTP execution")
	}
	local, err := executor.Open(*workspace, *state)
	if err != nil {
		return err
	}
	defer local.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go local.RunSweeper(ctx, *retain, time.Hour)
	if *stdio {
		if closer, ok := stdin.(io.Closer); ok {
			go func() { <-ctx.Done(); _ = closer.Close() }()
		}
		return servePipes(ctx, local, stdin, stdout)
	}
	server := &http.Server{Addr: *listen, Handler: executor.Handler(local, os.Getenv("GOLEM_NODE_TOKEN")), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: time.Minute}
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("execution endpoint listening", "address", *listen)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func servePipes(ctx context.Context, backend executor.Backend, in io.Reader, out io.Writer) error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	defer wg.Wait()
	encode := func(v any) { mu.Lock(); defer mu.Unlock(); _ = json.NewEncoder(out).Encode(v) }
	encode(map[string]bool{"ready": true})
	decoder := json.NewDecoder(in)
	slots := make(chan struct{}, 16)
	for {
		var req struct {
			ID     string        `json:"id"`
			Op     string        `json:"op"`
			Spec   executor.Spec `json:"spec"`
			Job    string        `json:"job"`
			Offset int64         `json:"offset"`
			Limit  int           `json:"limit"`
		}
		if err := decoder.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("invalid execution protocol")
		}
		if req.ID == "" {
			return errors.New("execution request is missing its ID")
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			var result any
			var err error
			switch req.Op {
			case "start":
				result, err = backend.Start(ctx, req.Spec)
			case "poll":
				result, err = backend.Poll(ctx, req.Job)
			case "stop":
				err = backend.Stop(ctx, req.Job)
				result = map[string]bool{"stopped": err == nil}
			case "output":
				result, err = backend.Output(ctx, req.Job, req.Offset, req.Limit)
			default:
				err = errors.New("unknown operation")
			}
			if err != nil {
				code := "execution_failed"
				message := "Execution request could not be completed."
				if errors.Is(err, executor.ErrOutputExpired) {
					code = "expired"
					message = "Output no longer retained."
				} else if errors.Is(err, executor.ErrUnavailable) {
					code = "unavailable"
					message = "Execution environment unavailable."
				}
				encode(map[string]any{"id": req.ID, "error": map[string]string{"code": code, "message": message}})
			} else {
				encode(map[string]any{"id": req.ID, "result": result})
			}
		}()
	}
}
