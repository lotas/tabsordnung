package diag

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lotas/tabsordnung/internal/applog"
	"github.com/lotas/tabsordnung/internal/server"
)

const defaultRuntimeLogInterval = 30 * time.Second

func ParseInterval(raw string) (time.Duration, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultRuntimeLogInterval, true, nil
	}
	if raw == "0" || strings.EqualFold(raw, "off") || strings.EqualFold(raw, "false") {
		return 0, false, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, convErr := strconv.Atoi(raw); convErr == nil {
			return time.Duration(secs) * time.Second, true, nil
		}
		return 0, false, err
	}
	if d <= 0 {
		return 0, false, nil
	}
	return d, true, nil
}

func StartRuntimeLogger(ctx context.Context, interval time.Duration, srv *server.Server) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		var mem runtime.MemStats
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runtime.ReadMemStats(&mem)
				stats := srv.Stats()
				applog.Info(
					"diag.runtime",
					"goroutines", runtime.NumGoroutine(),
					"heapAllocMB", fmt.Sprintf("%.2f", bytesToMB(mem.HeapAlloc)),
					"heapInuseMB", fmt.Sprintf("%.2f", bytesToMB(mem.HeapInuse)),
					"heapObjects", mem.HeapObjects,
					"stackInuseMB", fmt.Sprintf("%.2f", bytesToMB(mem.StackInuse)),
					"sysMB", fmt.Sprintf("%.2f", bytesToMB(mem.Sys)),
					"nextGCMB", fmt.Sprintf("%.2f", bytesToMB(mem.NextGC)),
					"numGC", mem.NumGC,
					"wsConnected", stats.Connected,
					"wsRecv", stats.Recv,
					"wsSend", stats.Send,
					"wsDropped", stats.Dropped,
					"wsQueueDepth", stats.QueueDepth,
				)
			}
		}
	}()
}

func StartPprofServer(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	go func() {
		applog.Info("diag.pprof.start", "addr", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			applog.Error("diag.pprof.listen", err, "addr", addr)
		}
	}()
}

func bytesToMB(v uint64) float64 {
	return float64(v) / (1024 * 1024)
}
