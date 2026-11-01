// Command mockupstream runs a deterministic OpenAI compatible fake LLM for benchmarks and demos.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/<your-user>/smartroute-core/internal/mockllm"
)

func main() {
	addr := flag.String("addr", ":9000", "listen address")
	latency := flag.Duration("latency", 500*time.Millisecond, "delay before the first byte")
	words := flag.Int("words", 24, "words per answer")
	wordDelay := flag.Duration("word-delay", 0, "delay between streamed words")
	errRate := flag.Float64("error-rate", 0, "fraction of requests that fail (0 to 1)")
	errCode := flag.Int("error-code", 500, "HTTP status returned for failures")
	failAfter := flag.Int64("fail-after", 0, "fail every request after the first N (0 disables)")
	flag.Parse()

	m := mockllm.New(mockllm.Config{
		Latency: *latency, Words: *words, WordDelay: *wordDelay,
		ErrorRate: *errRate, ErrorCode: *errCode, FailAfterN: *failAfter,
	})
	srv := &http.Server{Addr: *addr, Handler: m.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	fmt.Printf("mock upstream listening on %s (latency %s, %d words)\n", *addr, *latency, *words)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}
