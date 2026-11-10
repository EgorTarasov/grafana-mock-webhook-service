package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultAuto = "firing,firing,resolved,multi,warning,mixed,no_data,staging,resolved"

type config struct {
	Listen      string
	Target      string
	Receiver    string
	ExternalURL string
	Auth        Auth
	Auto        []string
	Interval    time.Duration
	Timeout     time.Duration
	Retries     int
}

func loadConfig() (config, error) {
	c := config{
		Listen:      env("LISTEN", ":3000"),
		Target:      env("TARGET_URL", "http://host.docker.internal:8080/webhooks/grafana"),
		Receiver:    env("RECEIVER", "larek-webhook"),
		ExternalURL: env("GRAFANA_URL", "https://grafana.k.larek.tech/"),
		Auth: Auth{
			User:     os.Getenv("AUTH_USER"),
			Password: os.Getenv("AUTH_PASSWORD"),
			Header:   os.Getenv("AUTH_HEADER"),
		},
	}
	var err error
	if c.Interval, err = time.ParseDuration(env("INTERVAL", "30s")); err != nil || c.Interval <= 0 {
		return c, fmt.Errorf("INTERVAL: ожидается длительность вроде 30s или 2m")
	}
	if c.Timeout, err = time.ParseDuration(env("TIMEOUT", "10s")); err != nil || c.Timeout <= 0 {
		return c, fmt.Errorf("TIMEOUT: ожидается длительность вроде 10s")
	}
	if c.Retries, err = strconv.Atoi(env("RETRIES", "2")); err != nil || c.Retries < 0 {
		return c, fmt.Errorf("RETRIES: ожидается целое число от 0")
	}
	auto := env("AUTO", defaultAuto)
	if v := strings.ToLower(strings.TrimSpace(auto)); v != "off" && v != "false" && v != "0" && v != "" {
		for _, name := range strings.Split(auto, ",") {
			name = strings.TrimSpace(name)
			if _, ok := findScenario(name); !ok {
				return c, fmt.Errorf("AUTO: нет сценария %q, список — команда list", name)
			}
			c.Auto = append(c.Auto, name)
		}
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func newApp(c config) *App {
	return &App{
		build: buildConfig{Receiver: c.Receiver, ExternalURL: c.ExternalURL},
		sender: &Sender{
			Client:  &http.Client{Timeout: c.Timeout},
			Auth:    c.Auth,
			Retries: c.Retries,
			Backoff: time.Second,
		},
		interval:    c.Interval,
		timeout:     c.Timeout,
		sequence:    c.Auto,
		target:      c.Target,
		autoEnabled: len(c.Auto) > 0,
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка настройки:", err)
		os.Exit(2)
	}

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		os.Exit(serve(cfg))
	case "list":
		for _, s := range scenarios {
			fmt.Printf("%-18s %s\n", s.Name, s.Title)
		}
	case "show":
		os.Exit(show(cfg, os.Args[2:]))
	case "send":
		os.Exit(sendOnce(cfg, os.Args[2:]))
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`grafana-mock-webhook-service — шлёт на ваш сервис вебхуки в формате Grafana Alerting.

Команды:
  serve                       веб-интерфейс на :3000 и автоотправка (по умолчанию)
  list                        список сценариев
  show <сценарий>             напечатать JSON сценария
  send [-target URL] <сценарий>  отправить один раз; код выхода 1, если ответ не 2xx

Переменные окружения: TARGET_URL, AUTO, INTERVAL, AUTH_USER, AUTH_PASSWORD,
AUTH_HEADER, RETRIES, TIMEOUT, RECEIVER, GRAFANA_URL, LISTEN — описание в README.
`)
}

func serve(cfg config) int {
	app := newApp(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: cfg.Listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go app.runAuto(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	auto := "выключена"
	if len(cfg.Auto) > 0 {
		auto = fmt.Sprintf("каждые %s: %s", cfg.Interval, strings.Join(cfg.Auto, ", "))
	}
	slog.Info("запущен", "ui", "http://localhost"+cfg.Listen, "target", cfg.Target, "auth", cfg.Auth.Kind(), "auto", auto)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("сервер остановился", "error", err)
		return 1
	}
	return 0
}

func show(cfg config, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "использование: show <сценарий>")
		return 2
	}
	body, err := newApp(cfg).payload(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "нет сценария %q, список — команда list\n", args[0])
		return 2
	}
	fmt.Println(string(body))
	return 0
}

func sendOnce(cfg config, args []string) int {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	target := fs.String("target", cfg.Target, "адрес получателя")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "использование: send [-target URL] <сценарий>")
		return 2
	}
	cfg.Target = *target
	slog.SetDefault(slog.New(slog.DiscardHandler))
	app := newApp(cfg)
	name := fs.Arg(0)
	body, err := app.payload(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "нет сценария %q, список — команда list\n", name)
		return 2
	}
	d := app.deliver(context.Background(), "cli", name, body)
	if d.Error != "" {
		fmt.Printf("%s → ошибка: %s (попыток: %d)\n", d.Target, d.Error, d.Attempts)
		return 1
	}
	fmt.Printf("%s → %d за %d мс (попыток: %d)\n%s\n", d.Target, d.StatusCode, d.DurationMS, d.Attempts, d.Response)
	if !d.OK {
		return 1
	}
	return 0
}
