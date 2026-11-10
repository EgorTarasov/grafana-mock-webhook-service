package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

type Auth struct {
	User     string
	Password string
	Header   string
}

func (a Auth) Kind() string {
	switch {
	case a.Header != "":
		return "header"
	case a.User != "":
		return "basic"
	default:
		return "none"
	}
}

type Sender struct {
	Client  *http.Client
	Auth    Auth
	Retries int
	Backoff time.Duration
}

type Result struct {
	StatusCode int
	Attempts   int
	Duration   time.Duration
	Response   string
	Err        error
}

func (r Result) OK() bool {
	return r.Err == nil && r.StatusCode >= 200 && r.StatusCode < 300
}

const maxResponseBody = 4 << 10

// Повторы как у Alertmanager-ресивера Grafana: сетевая ошибка и 5xx — повторяем,
// 4xx — нет, повтор того же запроса ничего не изменит.
func (s *Sender) Send(ctx context.Context, target string, body []byte) Result {
	start := time.Now()
	var res Result
	backoff := s.Backoff
	for attempt := 1; ; attempt++ {
		res = s.once(ctx, target, body)
		res.Attempts = attempt
		retry := res.Err != nil || res.StatusCode >= 500
		if !retry || attempt > s.Retries || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	res.Duration = time.Since(start)
	return res
}

func (s *Sender) once(ctx context.Context, target string, body []byte) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return Result{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Grafana")
	switch s.Auth.Kind() {
	case "header":
		req.Header.Set("Authorization", s.Auth.Header)
	case "basic":
		req.SetBasicAuth(s.Auth.User, s.Auth.Password)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return Result{Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{StatusCode: resp.StatusCode, Err: err}
	}
	return Result{StatusCode: resp.StatusCode, Response: string(data)}
}
