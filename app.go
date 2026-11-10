package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type Delivery struct {
	ID         int       `json:"id"`
	At         time.Time `json:"at"`
	Source     string    `json:"source"`
	Scenario   string    `json:"scenario"`
	Target     string    `json:"target"`
	StatusCode int       `json:"statusCode"`
	OK         bool      `json:"ok"`
	Attempts   int       `json:"attempts"`
	DurationMS int64     `json:"durationMs"`
	Error      string    `json:"error,omitempty"`
	Request    string    `json:"request"`
	Response   string    `json:"response"`
}

type App struct {
	build    buildConfig
	sender   *Sender
	interval time.Duration
	timeout  time.Duration
	sequence []string

	mu          sync.Mutex
	target      string
	autoEnabled bool
	autoIdx     int
	nextAt      time.Time
	lastID      int
	log         []Delivery
}

const logSize = 100

func (a *App) Target() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.target
}

func (a *App) payload(name string) ([]byte, error) {
	s, ok := findScenario(name)
	if !ok {
		return nil, errUnknownScenario
	}
	return json.MarshalIndent(buildMessage(a.build, s.build(), time.Now()), "", "  ")
}

var errUnknownScenario = errors.New("unknown scenario")

func (a *App) deliver(ctx context.Context, source, scenario string, body []byte) Delivery {
	target := a.Target()
	ctx, cancel := context.WithTimeout(ctx, a.timeout*time.Duration(a.sender.Retries+2))
	defer cancel()
	res := a.sender.Send(ctx, target, body)

	d := Delivery{
		At:         time.Now(),
		Source:     source,
		Scenario:   scenario,
		Target:     target,
		StatusCode: res.StatusCode,
		OK:         res.OK(),
		Attempts:   res.Attempts,
		DurationMS: res.Duration.Milliseconds(),
		Request:    string(body),
		Response:   res.Response,
	}
	if res.Err != nil {
		d.Error = res.Err.Error()
	}

	a.mu.Lock()
	a.lastID++
	d.ID = a.lastID
	a.log = append([]Delivery{d}, a.log...)
	if len(a.log) > logSize {
		a.log = a.log[:logSize]
	}
	a.mu.Unlock()

	attrs := []any{"scenario", scenario, "source", source, "target", target, "status", d.StatusCode, "attempts", d.Attempts, "ms", d.DurationMS}
	if d.OK {
		slog.Info("отправлено", attrs...)
	} else {
		slog.Warn("не доставлено", append(attrs, "error", d.Error)...)
	}
	return d
}

func (a *App) runAuto(ctx context.Context) {
	if len(a.sequence) == 0 {
		return
	}
	first := 3 * time.Second
	a.mu.Lock()
	a.nextAt = time.Now().Add(first)
	a.mu.Unlock()
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		a.mu.Lock()
		enabled := a.autoEnabled
		name := a.sequence[a.autoIdx%len(a.sequence)]
		if enabled {
			a.autoIdx++
		}
		a.nextAt = time.Now().Add(a.interval)
		a.mu.Unlock()

		if enabled {
			if body, err := a.payload(name); err == nil {
				a.deliver(ctx, "auto", name, body)
			}
		}
		timer.Reset(a.interval)
	}
}

type stateResponse struct {
	Target   string    `json:"target"`
	Auth     string    `json:"auth"`
	Auto     bool      `json:"auto"`
	Sequence []string  `json:"sequence"`
	Next     string    `json:"next"`
	Interval string    `json:"interval"`
	NextAt   time.Time `json:"nextAt"`
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		st := stateResponse{
			Target:   a.target,
			Auth:     a.sender.Auth.Kind(),
			Auto:     a.autoEnabled && len(a.sequence) > 0,
			Sequence: a.sequence,
			Interval: a.interval.String(),
			NextAt:   a.nextAt,
		}
		if len(a.sequence) > 0 {
			st.Next = a.sequence[a.autoIdx%len(a.sequence)]
		}
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, st)
	})

	mux.HandleFunc("POST /api/target", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Не удалось прочитать запрос")
			return
		}
		u, err := url.Parse(req.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeError(w, http.StatusBadRequest, "Адрес должен начинаться с http:// или https://")
			return
		}
		a.mu.Lock()
		a.target = req.URL
		a.mu.Unlock()
		slog.Info("адрес получателя изменён", "target", req.URL)
		writeJSON(w, http.StatusOK, map[string]string{"target": req.URL})
	})

	mux.HandleFunc("POST /api/auto", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Не удалось прочитать запрос")
			return
		}
		if req.Enabled && len(a.sequence) == 0 {
			writeError(w, http.StatusBadRequest, "Автоотправка выключена при запуске (AUTO=off)")
			return
		}
		a.mu.Lock()
		a.autoEnabled = req.Enabled
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": req.Enabled})
	})

	mux.HandleFunc("GET /api/scenarios", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, scenarios)
	})

	mux.HandleFunc("GET /api/scenarios/{name}", func(w http.ResponseWriter, r *http.Request) {
		body, err := a.payload(r.PathValue("name"))
		if err != nil {
			writeError(w, http.StatusNotFound, "Нет такого сценария")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})

	mux.HandleFunc("POST /api/send/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		body, err := a.payload(name)
		if err != nil {
			writeError(w, http.StatusNotFound, "Нет такого сценария")
			return
		}
		writeJSON(w, http.StatusOK, a.deliver(context.WithoutCancel(r.Context()), "manual", name, body))
	})

	mux.HandleFunc("POST /api/send-raw", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, "Не удалось прочитать запрос")
			return
		}
		if !json.Valid(body) {
			writeError(w, http.StatusBadRequest, "Это не JSON — проверьте скобки и кавычки")
			return
		}
		writeJSON(w, http.StatusOK, a.deliver(context.WithoutCancel(r.Context()), "manual", "свой JSON", body))
	})

	mux.HandleFunc("GET /api/deliveries", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		out := append([]Delivery(nil), a.log...)
		a.mu.Unlock()
		if out == nil {
			out = []Delivery{}
		}
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("DELETE /api/deliveries", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		a.log = nil
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
