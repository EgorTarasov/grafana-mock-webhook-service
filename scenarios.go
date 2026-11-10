package main

import "time"

type Scenario struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	build       func() groupSpec
}

func errorRateLabels(service, folder string) map[string]string {
	return map[string]string{
		"alertname":      "HighErrorRate",
		"grafana_folder": folder,
		"namespace":      folder + "-" + service,
		"service":        service,
		"severity":       "critical",
	}
}

func errorRateFiring(service, folder string, rate float64) alertSpec {
	return alertSpec{
		labels: errorRateLabels(service, folder),
		annotations: map[string]string{
			"summary":     "Доля 5xx у " + service + " выше 5%",
			"description": "За последние 5 минут " + formatFloat(rate*100) + "% запросов завершились ошибкой 5xx",
			"runbook_url": "https://docs.k.larek.tech/runbooks/high-error-rate",
		},
		startedAgo: 3 * time.Minute,
		values:     map[string]float64{"A": rate, "B": 1},
		rule:       "aez1k2",
		dashboard:  "red-overview",
		panel:      4,
	}
}

func errorRateResolved(service, folder string, rate float64) alertSpec {
	return alertSpec{
		labels: errorRateLabels(service, folder),
		annotations: map[string]string{
			"summary":     "Доля 5xx у " + service + " выше 5%",
			"description": "Доля ошибок вернулась к норме",
		},
		resolved:   true,
		startedAgo: 25 * time.Minute,
		values:     map[string]float64{"A": rate, "B": 0},
		rule:       "aez1k2",
		dashboard:  "red-overview",
		panel:      4,
	}
}

func latencyFiring(service string, p99 float64) alertSpec {
	return alertSpec{
		labels: map[string]string{
			"alertname":      "HighLatency",
			"grafana_folder": "prod",
			"namespace":      "prod-" + service,
			"service":        service,
			"severity":       "warning",
		},
		annotations: map[string]string{
			"summary":     "p99 задержки у " + service + " выше 1 с",
			"description": "p99 за 10 минут — " + formatFloat(p99) + " с",
		},
		startedAgo: 8 * time.Minute,
		values:     map[string]float64{"A": p99, "B": 1},
		rule:       "bq7x3m",
		dashboard:  "red-overview",
		panel:      6,
	}
}

var scenarios = []Scenario{
	{
		Name:        "firing",
		Title:       "Алерт загорелся",
		Description: "Один critical-алерт HighErrorRate у eggkiller в prod. Повторная отправка — тот же fingerprint, как повторное уведомление Grafana.",
		build: func() groupSpec {
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				alerts:  []alertSpec{errorRateFiring("eggkiller", "prod", 0.073)},
			}
		},
	},
	{
		Name:        "resolved",
		Title:       "Алерт прошёл",
		Description: "Тот же алерт eggkiller в статусе resolved: fingerprint совпадает с firing, endsAt заполнен.",
		build: func() groupSpec {
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				alerts:  []alertSpec{errorRateResolved("eggkiller", "prod", 0.011)},
			}
		},
	},
	{
		Name:        "mixed",
		Title:       "Один горит, другой прошёл",
		Description: "В одной группе firing у eggkiller и resolved у calc. Верхний status — firing.",
		build: func() groupSpec {
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				alerts: []alertSpec{
					errorRateFiring("eggkiller", "prod", 0.073),
					errorRateResolved("calc", "prod", 0.012),
				},
			}
		},
	},
	{
		Name:        "multi",
		Title:       "Несколько алертов в группе",
		Description: "HighLatency warning сразу у трёх сервисов, сгруппировано по alertname.",
		build: func() groupSpec {
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				alerts: []alertSpec{
					latencyFiring("eggkiller", 1.8),
					latencyFiring("calc", 2.4),
					latencyFiring("algo", 1.2),
				},
			}
		},
	},
	{
		Name:        "warning",
		Title:       "Предупреждение (warning)",
		Description: "Под calc перезапускается. Severity warning — для проверки уровней уведомлений.",
		build: func() groupSpec {
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				alerts: []alertSpec{{
					labels: map[string]string{
						"alertname":      "PodRestarting",
						"grafana_folder": "prod",
						"namespace":      "prod-calc",
						"service":        "calc",
						"pod":            "calc-7d9f8b6c5-x2k4p",
						"severity":       "warning",
					},
					annotations: map[string]string{
						"summary":     "Под calc перезапустился 4 раза за 15 минут",
						"description": "Последняя причина: OOMKilled",
					},
					startedAgo: 15 * time.Minute,
					values:     map[string]float64{"A": 4, "B": 1},
					rule:       "c81kq0",
					dashboard:  "k8s-pods",
					panel:      2,
				}},
			}
		},
	},
	{
		Name:        "info",
		Title:       "Информационный (info)",
		Description: "Сертификат скоро истекает. Severity info — самый низкий уровень.",
		build: func() groupSpec {
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				alerts: []alertSpec{{
					labels: map[string]string{
						"alertname":      "CertificateExpiresSoon",
						"grafana_folder": "prod",
						"host":           "larek.tech",
						"severity":       "info",
					},
					annotations: map[string]string{
						"summary":     "Сертификат larek.tech истекает через 14 дней",
						"description": "Продление обычно проходит автоматически за 30 дней до срока",
					},
					startedAgo: 2 * time.Hour,
					values:     map[string]float64{"A": 14, "B": 1},
					rule:       "d0tls9",
				}},
			}
		},
	},
	{
		Name:        "staging",
		Title:       "Алерт из staging",
		Description: "HighErrorRate у eggkiller, но в папке staging — для проверки маршрутизации по разным чатам.",
		build: func() groupSpec {
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				alerts:  []alertSpec{errorRateFiring("eggkiller", "staging", 0.21)},
			}
		},
	},
	{
		Name:        "no_data",
		Title:       "Нет данных (no_data)",
		Description: "Grafana не получила данные для правила: alertname DatasourceNoData, values = null, state = no_data.",
		build: func() groupSpec {
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				state:   "no_data",
				alerts: []alertSpec{{
					labels: map[string]string{
						"alertname":      "DatasourceNoData",
						"datasource_uid": "prometheus",
						"grafana_folder": "prod",
						"ref_id":         "A",
						"rulename":       "HighErrorRate",
						"service":        "eggkiller",
						"severity":       "critical",
					},
					annotations: map[string]string{
						"summary": "Доля 5xx у eggkiller выше 5%",
					},
					startedAgo: 5 * time.Minute,
					rule:       "aez1k2",
				}},
			}
		},
	},
	{
		Name:        "datasource_error",
		Title:       "Ошибка источника данных",
		Description: "Запрос правила упал: alertname DatasourceError, текст ошибки в annotations.Error.",
		build: func() groupSpec {
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				alerts: []alertSpec{{
					labels: map[string]string{
						"alertname":      "DatasourceError",
						"datasource_uid": "prometheus",
						"grafana_folder": "prod",
						"ref_id":         "A",
						"rulename":       "HighErrorRate",
						"service":        "eggkiller",
						"severity":       "critical",
					},
					annotations: map[string]string{
						"Error":   "[sse.dataQueryError] failed to execute query [A]: Post \"http://prometheus:9090/api/v1/query_range\": context deadline exceeded",
						"summary": "Доля 5xx у eggkiller выше 5%",
					},
					startedAgo: 2 * time.Minute,
					rule:       "aez1k2",
				}},
			}
		},
	},
	{
		Name:        "empty_values",
		Title:       "Пустые values",
		Description: "Обычный firing-алерт, но values = null и valueString пустой.",
		build: func() groupSpec {
			a := errorRateFiring("eggkiller", "prod", 0)
			a.values = nil
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				alerts:  []alertSpec{a},
			}
		},
	},
	{
		Name:        "truncated",
		Title:       "Обрезанный список",
		Description: "В запросе 3 алерта, ещё 7 Grafana отбросила по max_alerts: truncatedAlerts = 7.",
		build: func() groupSpec {
			return groupSpec{
				groupBy:   []string{"alertname", "grafana_folder"},
				truncated: 7,
				alerts: []alertSpec{
					latencyFiring("eggkiller", 1.8),
					latencyFiring("calc", 2.4),
					latencyFiring("algo", 1.2),
				},
			}
		},
	},
	{
		Name:        "custom_template",
		Title:       "Свой шаблон title/message",
		Description: "В контакт-поинте задан свой шаблон: title и message приходят с текстом пользователя, а не по умолчанию.",
		build: func() groupSpec {
			return groupSpec{
				groupBy: []string{"alertname", "grafana_folder"},
				alerts:  []alertSpec{errorRateFiring("eggkiller", "prod", 0.073)},
				title:   "eggkiller: 5xx 7.3%",
				message: "eggkiller в prod отдаёт 7.3% ошибок 5xx. Runbook: https://docs.k.larek.tech/runbooks/high-error-rate",
			}
		},
	},
}

func findScenario(name string) (Scenario, bool) {
	for _, s := range scenarios {
		if s.Name == name {
			return s, true
		}
	}
	return Scenario{}, false
}
