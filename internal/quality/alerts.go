package quality

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
)

var alertMetrics = []string{"traces", "error_rate", "fail_rate", "p95_latency_ms", "cost_usd", "feedback_mean"}

// ValidateAlert normalizes an alert rule: metric over a rolling window
// compared with a threshold, optionally notifying a webhook (the payload's
// "text" field makes it compatible with Slack and Mattermost incoming hooks).
func ValidateAlert(raw M) (M, error) {
	name := strings.TrimSpace(domain.Str(raw["name"]))
	if name == "" || len(name) > 200 {
		return nil, errors.New("name must be 1-200 characters")
	}
	metric := domain.Str(raw["metric"])
	if !domain.Has(alertMetrics, metric) {
		return nil, errors.New("metric must be one of " + strings.Join(alertMetrics, ", "))
	}
	op := domain.Str(raw["op"])
	if op == "" {
		op = ">"
	}
	if op != ">" && op != "<" {
		return nil, errors.New("op must be > or <")
	}
	if !finite(raw["threshold"]) {
		return nil, errors.New("threshold must be a number")
	}
	window := domain.Int(raw["window_minutes"])
	if window == 0 {
		window = 15
	}
	if window < 1 || window > 7*24*60 {
		return nil, errors.New("window_minutes must be 1-10080")
	}
	min := domain.Int(raw["min_samples"])
	if min < 0 || min > 100000 {
		return nil, errors.New("min_samples must be 0-100000")
	}
	feedback := strings.TrimSpace(domain.Str(raw["feedback_name"]))
	if metric == "feedback_mean" && feedback == "" {
		return nil, errors.New("feedback_mean needs feedback_name")
	}
	hook := strings.TrimSpace(domain.Str(raw["webhook_url"]))
	if hook != "" {
		u, err := url.Parse(hook)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("webhook_url must be an http(s) URL")
		}
	}
	enabled := true
	if v, ok := raw["enabled"].(bool); ok {
		enabled = v
	}
	return M{"name": name, "metric": metric, "op": op, "threshold": domain.Num(raw["threshold"]), "window_minutes": window, "min_samples": min, "service": strings.TrimSpace(domain.Str(raw["service"])), "feedback_name": feedback, "webhook_url": hook, "enabled": enabled}, nil
}

// Measure computes a rule's metric over its window; n is the sample size.
func Measure(ctx context.Context, db *store.Store, rule M) (value any, n int, err error) {
	since := domain.Now() - domain.Num(rule["window_minutes"])*60
	service := domain.Str(rule["service"])
	if rule["metric"] == "feedback_mean" {
		err = db.Pool.QueryRow(ctx, `SELECT avg(f.value)::float8, count(f.value)::int FROM arl.feedback f JOIN arl.traces t ON t.id=f.trace_id JOIN arl.sessions s ON s.id=t.session_id
 WHERE f.project_id=$1 AND f.name=$2 AND extract(epoch from f.created_at)>=$3 AND ($4='' OR s.service=$4)`, db.Project, rule["feedback_name"], since, service).Scan(&value, &n)
		return
	}
	var traces, errored, failed, judged int
	var cost float64
	var p95 *float64
	err = db.Pool.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE t.status='failed' OR t.errors>0)::int, count(*) FILTER (WHERE t.verdict='fail')::int,
 count(*) FILTER (WHERE t.verdict IN ('pass','fail'))::int, COALESCE(sum(t.cost_usd),0)::float8, percentile_cont(0.95) WITHIN GROUP (ORDER BY t.duration_ms)
 FROM arl.traces t JOIN arl.sessions s ON s.id=t.session_id WHERE t.project_id=$1 AND t.started_at>=$2 AND ($3='' OR s.service=$3)`, db.Project, since, service).Scan(&traces, &errored, &failed, &judged, &cost, &p95)
	if err != nil {
		return nil, 0, err
	}
	switch rule["metric"] {
	case "traces":
		return float64(traces), traces, nil
	case "error_rate":
		if traces > 0 {
			value = float64(errored) / float64(traces)
		}
		return value, traces, nil
	case "fail_rate":
		if judged > 0 {
			value = float64(failed) / float64(judged)
		}
		return value, judged, nil
	case "p95_latency_ms":
		if p95 != nil {
			value = *p95
		}
		return value, traces, nil
	}
	return cost, traces, nil
}

var hookClient = &http.Client{Timeout: 10 * time.Second}

// Check evaluates every enabled rule of the project (job alerts.check) and
// records ok → firing → resolved transitions, notifying webhooks on change.
func Check(ctx context.Context, db *store.Store) (M, error) {
	rules, err := db.List(ctx, Alerts, M{"enabled": true}, 500)
	if err != nil {
		return nil, err
	}
	fired, resolved := 0, 0
	for _, rule := range rules {
		value, n, err := Measure(ctx, db, rule)
		if err != nil {
			return nil, err
		}
		state := "ok"
		switch {
		case value == nil || n < domain.Int(rule["min_samples"]):
			state = "insufficient_data"
		case rule["op"] == ">" && domain.Num(value) > domain.Num(rule["threshold"]):
			state = "firing"
		case rule["op"] == "<" && domain.Num(value) < domain.Num(rule["threshold"]):
			state = "firing"
		}
		previous := domain.Str(rule["state"])
		id := domain.Str(rule["id"])
		transition := ""
		if state == "firing" && previous != "firing" {
			transition, fired = "firing", fired+1
		} else if state != "firing" && previous == "firing" {
			transition, resolved = "resolved", resolved+1
		}
		_, err = db.Update(ctx, Alerts, id, func(r M) error {
			r["state"], r["last_value"], r["last_samples"], r["last_checked_at"] = state, value, n, domain.Now()
			if transition == "firing" {
				r["last_fired_at"] = domain.Now()
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if transition == "" {
			continue
		}
		event := M{"rule_id": id, "name": rule["name"], "state": transition, "metric": rule["metric"], "value": value, "threshold": rule["threshold"], "op": rule["op"], "samples": n, "service": rule["service"], "window_minutes": rule["window_minutes"], "at": domain.Now()}
		if hook := domain.Str(rule["webhook_url"]); hook != "" {
			event["delivered"] = notify(ctx, hook, event) == nil
		}
		if err = db.Put(ctx, AlertEvents, domain.ID("alev"), event); err != nil {
			return nil, err
		}
	}
	return M{"rules": len(rules), "fired": fired, "resolved": resolved}, nil
}

func notify(ctx context.Context, hook string, event M) error {
	icon := map[string]string{"firing": "🔥", "resolved": "✅"}[domain.Str(event["state"])]
	scope := ""
	if s := domain.Str(event["service"]); s != "" {
		scope = " · " + s
	}
	text := fmt.Sprintf("%s [ARL] %s %s: %s = %v (threshold %s %v, %v samples, last %v min%s)", icon, event["name"], event["state"], event["metric"], round(event["value"]), event["op"], event["threshold"], event["samples"], event["window_minutes"], scope)
	body := domain.Copy(event)
	body["text"] = text
	req, err := http.NewRequestWithContext(ctx, "POST", hook, bytes.NewReader(domain.JSON(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := hookClient.Do(req)
	if err != nil {
		slog.Warn("alert webhook unreachable", "rule", event["name"])
		return err
	}
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", res.StatusCode)
	}
	return nil
}

func round(v any) any {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.4g", domain.Num(v))
}
