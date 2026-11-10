package main

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Alert struct {
	Status       string             `json:"status"`
	Labels       map[string]string  `json:"labels"`
	Annotations  map[string]string  `json:"annotations"`
	StartsAt     time.Time          `json:"startsAt"`
	EndsAt       time.Time          `json:"endsAt"`
	GeneratorURL string             `json:"generatorURL"`
	Fingerprint  string             `json:"fingerprint"`
	SilenceURL   string             `json:"silenceURL"`
	DashboardURL string             `json:"dashboardURL"`
	PanelURL     string             `json:"panelURL"`
	Values       map[string]float64 `json:"values"`
	ValueString  string             `json:"valueString"`
}

type Message struct {
	Receiver          string            `json:"receiver"`
	Status            string            `json:"status"`
	OrgID             int               `json:"orgId"`
	Alerts            []Alert           `json:"alerts"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Title             string            `json:"title"`
	State             string            `json:"state"`
	Message           string            `json:"message"`
}

type alertSpec struct {
	labels      map[string]string
	annotations map[string]string
	resolved    bool
	startedAgo  time.Duration
	endedAgo    time.Duration
	values      map[string]float64
	rule        string
	dashboard   string
	panel       int
}

type groupSpec struct {
	groupBy   []string
	alerts    []alertSpec
	truncated int
	state     string
	title     string
	message   string
}

type buildConfig struct {
	Receiver    string
	ExternalURL string
}

func buildMessage(cfg buildConfig, g groupSpec, now time.Time) Message {
	now = now.UTC().Truncate(time.Second)
	ext := strings.TrimRight(cfg.ExternalURL, "/") + "/"

	alerts := make([]Alert, 0, len(g.alerts))
	firing := 0
	for _, s := range g.alerts {
		a := Alert{
			Status:       "firing",
			Labels:       s.labels,
			Annotations:  s.annotations,
			StartsAt:     now.Add(-s.startedAgo),
			GeneratorURL: fmt.Sprintf("%salerting/grafana/%s/view?orgId=1", ext, s.rule),
			Fingerprint:  fingerprint(s.labels),
			SilenceURL:   silenceURL(ext, s.labels),
			Values:       s.values,
			ValueString:  valueString(s.values, s.labels),
		}
		if a.Annotations == nil {
			a.Annotations = map[string]string{}
		}
		if s.resolved {
			a.Status = "resolved"
			a.EndsAt = now.Add(-s.endedAgo)
		} else {
			firing++
		}
		if s.dashboard != "" {
			a.DashboardURL = fmt.Sprintf("%sd/%s?orgId=1", ext, s.dashboard)
			if s.panel > 0 {
				a.PanelURL = fmt.Sprintf("%s&viewPanel=%d", a.DashboardURL, s.panel)
			}
		}
		alerts = append(alerts, a)
	}

	groupLabels := map[string]string{}
	for _, name := range g.groupBy {
		if len(alerts) > 0 {
			if v, ok := alerts[0].Labels[name]; ok {
				groupLabels[name] = v
			}
		}
	}

	m := Message{
		Receiver:          cfg.Receiver,
		Status:            "resolved",
		OrgID:             1,
		Alerts:            alerts,
		GroupLabels:       groupLabels,
		CommonLabels:      commonPairs(alerts, func(a Alert) map[string]string { return a.Labels }),
		CommonAnnotations: commonPairs(alerts, func(a Alert) map[string]string { return a.Annotations }),
		ExternalURL:       ext,
		Version:           "1",
		GroupKey:          groupKey(cfg.Receiver, groupLabels),
		TruncatedAlerts:   g.truncated,
		State:             "ok",
	}
	if firing > 0 {
		m.Status = "firing"
		m.State = "alerting"
	}
	if g.state != "" {
		m.State = g.state
	}
	m.Title = defaultTitle(m, firing)
	if g.title != "" {
		m.Title = g.title
	}
	m.Message = defaultMessageText(alerts)
	if g.message != "" {
		m.Message = g.message
	}
	return m
}

// Как у Grafana: отпечаток зависит только от набора лейблов, поэтому firing и
// resolved одного алерта получают одинаковый fingerprint.
func fingerprint(labels map[string]string) string {
	h := fnv.New64a()
	for _, k := range sortedKeys(labels) {
		h.Write([]byte(k))
		h.Write([]byte{0xff})
		h.Write([]byte(labels[k]))
		h.Write([]byte{0xff})
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

func silenceURL(ext string, labels map[string]string) string {
	var b strings.Builder
	b.WriteString(ext + "alerting/silence/new?alertmanager=grafana")
	for _, k := range sortedKeys(labels) {
		b.WriteString("&matcher=" + url.QueryEscape(k+"="+labels[k]))
	}
	b.WriteString("&orgId=1")
	return b.String()
}

func valueString(values map[string]float64, labels map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	sel := ""
	if svc, ok := labels["service"]; ok {
		sel = "service=" + svc
	}
	parts := make([]string, 0, len(values))
	for _, k := range sortedKeys(values) {
		parts = append(parts, fmt.Sprintf("[ var='%s' labels={%s} value=%s ]", k, sel, formatFloat(values[k])))
	}
	return strings.Join(parts, ", ")
}

func commonPairs(alerts []Alert, get func(Alert) map[string]string) map[string]string {
	out := map[string]string{}
	if len(alerts) == 0 {
		return out
	}
	for k, v := range get(alerts[0]) {
		out[k] = v
	}
	for _, a := range alerts[1:] {
		m := get(a)
		for k, v := range out {
			if m[k] != v {
				delete(out, k)
			}
		}
	}
	return out
}

func groupKey(receiver string, groupLabels map[string]string) string {
	pairs := make([]string, 0, len(groupLabels))
	for _, k := range sortedKeys(groupLabels) {
		pairs = append(pairs, fmt.Sprintf("%s=%q", k, groupLabels[k]))
	}
	return fmt.Sprintf(`{}/{__grafana_autogenerated__="true"}/{__grafana_receiver__=%q}:{%s}`, receiver, strings.Join(pairs, ", "))
}

// Повторяет шаблон default.title из Grafana.
func defaultTitle(m Message, firing int) string {
	var b strings.Builder
	b.WriteString("[" + strings.ToUpper(m.Status))
	if m.Status == "firing" {
		b.WriteString(":" + strconv.Itoa(firing))
		if resolved := len(m.Alerts) - firing; resolved > 0 {
			b.WriteString(", RESOLVED:" + strconv.Itoa(resolved))
		}
	}
	b.WriteString("] ")
	b.WriteString(strings.Join(sortedValues(m.GroupLabels), " "))
	if len(m.CommonLabels) > len(m.GroupLabels) {
		rest := map[string]string{}
		for k, v := range m.CommonLabels {
			if _, ok := m.GroupLabels[k]; !ok {
				rest[k] = v
			}
		}
		b.WriteString(" (" + strings.Join(sortedValues(rest), " ") + ")")
	}
	return b.String()
}

func defaultMessageText(alerts []Alert) string {
	var b strings.Builder
	section := func(title, status string) {
		var list []Alert
		for _, a := range alerts {
			if a.Status == status {
				list = append(list, a)
			}
		}
		if len(list) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("**" + title + "**\n")
		for _, a := range list {
			b.WriteString("\n")
			if len(a.Values) > 0 {
				vals := make([]string, 0, len(a.Values))
				for _, k := range sortedKeys(a.Values) {
					vals = append(vals, k+"="+formatFloat(a.Values[k]))
				}
				b.WriteString("Value: " + strings.Join(vals, ", ") + "\n")
			} else {
				b.WriteString("Value: [no value]\n")
			}
			b.WriteString("Labels:\n")
			for _, k := range sortedKeys(a.Labels) {
				b.WriteString(" - " + k + " = " + a.Labels[k] + "\n")
			}
			if len(a.Annotations) > 0 {
				b.WriteString("Annotations:\n")
				for _, k := range sortedKeys(a.Annotations) {
					b.WriteString(" - " + k + " = " + a.Annotations[k] + "\n")
				}
			}
			b.WriteString("Source: " + a.GeneratorURL + "\n")
			b.WriteString("Silence: " + a.SilenceURL + "\n")
			if a.DashboardURL != "" {
				b.WriteString("Dashboard: " + a.DashboardURL + "\n")
			}
			if a.PanelURL != "" {
				b.WriteString("Panel: " + a.PanelURL + "\n")
			}
		}
	}
	section("Firing", "firing")
	section("Resolved", "resolved")
	return b.String()
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}
