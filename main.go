package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ory/graceful"
	"github.com/slack-go/slack"
	"hash/fnv"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var webhookUrl string
var username string
var grafanaAlertSource bool
var grafanaUrl string
var disableGrafanaSilenceButton bool

func main() {
	flag.StringVar(&webhookUrl, "webhook-url", "", "Slack webhook url")
	flag.StringVar(&username, "username", "Grafana", "Slack username")
	flag.BoolVar(&grafanaAlertSource, "grafanaAlertSource", true, "Set to false to use alerter with external alert manager")
	flag.StringVar(&grafanaUrl, "grafanaUrl", "", "URL to grafana (applicable only when grafanaAlertSource=false)")
	flag.BoolVar(&disableGrafanaSilenceButton, "grafanaSilenceButton", true, "Set to false to enable silence button in the alert message")
	flag.Parse()

	http.HandleFunc("/slack", handleWebhookRequest)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := graceful.WithDefaults(&http.Server{
		Addr:    ":8080",
		Handler: http.DefaultServeMux,
	})

	http.DefaultTransport = LoggingRoundTripper{http.DefaultTransport}

	log.Println("starting the server")
	if err := graceful.Graceful(server.ListenAndServe, server.Shutdown); err != nil {
		log.Fatalln("failed to gracefully shutdown")
	}
	log.Println("server stopped")
}

type LoggingRoundTripper struct {
	Proxied http.RoundTripper
}

func (l LoggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	reqDump, _ := httputil.DumpRequest(req, true)
	res, err := l.Proxied.RoundTrip(req)
	if res == nil {
		log.Println("nil res", err)
	} else if res.StatusCode != http.StatusOK {
		resDump, _ := httputil.DumpResponse(res, true)
		log.Println("err request", string(reqDump))
		log.Println("err response", string(resDump))
	}
	return res, err
}

func handleWebhookRequest(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = "alerts"
		log.Println("slack channel is not specified in 'channel' query param, using default 'alerts' channel")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	grafanaMsg := GrafanaMsg{}
	if err := json.Unmarshal(body, &grafanaMsg); err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slackMsgs := buildMessages(grafanaMsg, channel)

	var lastError error
	for _, slackMsg := range slackMsgs {
		if err := slack.PostWebhookContext(r.Context(), webhookUrl, &slackMsg); err != nil {
			lastError = err
			log.Println(err)
		}
	}
	if lastError != nil {
		http.Error(w, lastError.Error(), http.StatusInternalServerError)
	} else {
		w.WriteHeader(http.StatusOK)
	}
}

func buildMessages(msg GrafanaMsg, channel string) []slack.WebhookMessage {
	var messages []slack.WebhookMessage

	alertsByStatus := groupByStatus(msg)

	for _, groupedAlerts := range alertsByStatus {

		chunkedAlerts := chunkBy(groupedAlerts, 7)

		for _, alerts := range chunkedAlerts {

			var firedText string
			var resolvedText string
			var blocks []slack.Block

			for i, alert := range alerts {
				var summary string
				if alert.Status != "resolved" {
					summary = ":sos: " + alert.Annotations["summary"]
					firedText = fmt.Sprintf("%s[%s] ", firedText, alert.Annotations["summary"])
				} else {
					summary = ":large_green_circle: " + alert.Annotations["summary"]
					resolvedText = fmt.Sprintf("%s[%s] ", resolvedText, alert.Annotations["summary"])
				}

				var buttons []slack.BlockElement

				generatorButton := slack.NewButtonBlockElement("generator", "", slack.NewTextBlockObject("plain_text", ":information_source: Details", true, false))
				if grafanaAlertSource {
					generatorButton.URL = alert.GeneratorURL
				} else {
					var labels []string
					for k, v := range alert.Labels {
						labels = append(labels, fmt.Sprintf(`%s="%s"`, k, v))
					}
					query := fmt.Sprintf("{%s}", strings.Join(labels, ","))
					generatorButton.URL = fmt.Sprintf("%s/alerting/list?queryString=%s&ruleType=alerting", grafanaUrl, url.QueryEscape(query))
				}
				generatorButton.Style = slack.StylePrimary
				buttons = append(buttons, generatorButton)

				if !grafanaAlertSource {
					parsed, err := url.ParseRequestURI(strings.TrimSuffix(alert.GeneratorURL, `\u0026g0.tab=1`))
					if err != nil {
						log.Println(err)
					} else {
						exploreButton := slack.NewButtonBlockElement("explore", "", slack.NewTextBlockObject("plain_text", ":chart_with_upwards_trend: Explore", true, false))
						expStr := fmt.Sprintf(`{"datasource":"prometheus","queries":[{"datasource":"Prometheus","expr":"%s","refId":"A"}],"range":{"from":"now-1h","to":"now"}}`, strings.ReplaceAll(parsed.Query().Get("g0.expr"), `"`, `\"`))
						exploreButton.URL = fmt.Sprintf("%s/explore?left=%s", grafanaUrl, url.QueryEscape(expStr))
						exploreButton.Style = slack.StylePrimary
						buttons = append(buttons, exploreButton)
					}
				}

				if alert.Status != "resolved" {
					if runbookUrl, ok := alert.Annotations["runbook_url"]; ok && runbookUrl != "" {
						runbookButton := slack.NewButtonBlockElement("runbook", "", slack.NewTextBlockObject("plain_text", ":page_with_curl: Runbook", true, false))
						runbookButton.URL = runbookUrl
						runbookButton.Style = slack.StyleDefault
						buttons = append(buttons, runbookButton)
					}
				}

				if alert.Status != "resolved" && !disableGrafanaSilenceButton {
					silenceButton := slack.NewButtonBlockElement("silence", "", slack.NewTextBlockObject("plain_text", ":no_bell: Silence", true, false))
					if grafanaAlertSource {
						silenceButton.URL = alert.SilenceURL
					} else {
						var matchers []string
						for k, v := range alert.Labels {
							matcher := fmt.Sprintf("%s=%s", k, v)
							matchers = append(matchers, fmt.Sprintf(`matcher=%s`, url.QueryEscape(matcher)))
						}
						silenceButton.URL = fmt.Sprintf("%s/alerting/silence/new?alertmanager=Alertmanager&%s", grafanaUrl, strings.Join(matchers, "&"))
					}
					silenceButton.Style = slack.StyleDanger
					buttons = append(buttons, silenceButton)
				}

				var contextElements []slack.MixedElement
				if alert.ValueString != "" {
					contextElements = append(contextElements, slack.NewTextBlockObject("plain_text", fmt.Sprintf("Value: %s", extractValue(alert.ValueString)), true, false))
				}
				contextElements = append(contextElements, slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("<!date^%d^Started at: {date_num} {time_secs}|_>", alert.StartsAt.Unix()), false, false))
				if !alert.EndsAt.IsZero() {
					contextElements = append(contextElements, slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("<!date^%d^Ended at: {date_num} {time_secs}|_>", alert.EndsAt.Unix()), false, false))
				}

				if i != 0 {
					blocks = append(blocks, slack.NewDividerBlock())
				}

				blocks = append(blocks, slack.NewHeaderBlock(slack.NewTextBlockObject("plain_text", summary, true, false)))

				if description, ok := alert.Annotations["description"]; ok && description != "" {
					blocks = append(blocks, slack.NewSectionBlock(slack.NewTextBlockObject("mrkdwn", description, false, false), nil, nil))
				}

				for name, value := range alert.Labels {
					if name == "label_app_kubernetes_io_team" {
						alert.Labels[name] = "@" + value
					}
				}
				labelsJson, err := json.Marshal(alert.Labels)
				if err != nil {
					log.Println(err)
					labelsJson = []byte{}
				}
				labelsStr := string(labelsJson)
				labelsStr = strings.ReplaceAll(strings.ReplaceAll(labelsStr, `":"`, `": "`), `","`, `", "`)
				blocks = append(blocks, slack.NewSectionBlock(slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("```%s```", labelsStr), false, false), nil, nil))

				blocks = append(blocks, slack.NewActionBlock(fmt.Sprintf("actions-%s", hash(alert.Labels)), buttons...))
				blocks = append(blocks, slack.NewContextBlock(fmt.Sprintf("context-%s", hash(alert.Labels)), contextElements...))
			}

			var previewText string
			if firedText != "" {
				previewText = fmt.Sprintf("Fired: %s", firedText)
			} else if resolvedText != "" {
				previewText = fmt.Sprintf("Resolved: %s", resolvedText)
			}

			messages = append(messages, slack.WebhookMessage{
				Username: username,
				Channel:  channel,
				Text:     previewText,
				Blocks:   &slack.Blocks{BlockSet: blocks},
			})
		}
	}

	return messages
}

func groupByStatus(msg GrafanaMsg) map[string][]Alert {
	grouped := map[string][]Alert{}
	for _, alert := range msg.Alerts {
		if alerts, ok := grouped[alert.Status]; ok {
			grouped[alert.Status] = append(alerts, alert)
			continue
		}
		grouped[alert.Status] = []Alert{alert}
	}
	return grouped
}

func extractValue(valueString string) string {
	// [ var='B' labels={job_name=XXX, namespace=yyy} value=123456 ]
	parts := strings.Split(valueString, "value=")
	if len(parts) != 2 {
		log.Printf("cannot split value by 'value=': %s", valueString)
		return valueString
	}
	value := strings.Split(parts[1], " ")
	if len(value) == 0 {
		log.Printf("cannot split value by ' ': %s", valueString)
		return valueString
	}
	str, err := humanize(value[0])
	if err != nil {
		log.Printf("cannot humanize value: %s", value[0])
		return value[0]
	}
	return str
}

func humanize(i string) (string, error) {
	v, err := strconv.ParseFloat(i, 64)
	if err != nil {
		return "", err
	}
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Sprintf("%.4g", v), nil
	}
	if math.Abs(v) >= 1 {
		prefix := ""
		for _, p := range []string{"k", "M", "G", "T", "P", "E", "Z", "Y"} {
			if math.Abs(v) < 1000 {
				break
			}
			prefix = p
			v /= 1000
		}
		return fmt.Sprintf("%.4g%s", v, prefix), nil
	}
	prefix := ""
	for _, p := range []string{"m", "u", "n", "p", "f", "a", "z", "y"} {
		if math.Abs(v) >= 1 {
			break
		}
		prefix = p
		v *= 1000
	}
	return fmt.Sprintf("%.4g%s", v, prefix), nil
}

func chunkBy[T any](items []T, chunkSize int) (chunks [][]T) {
	for chunkSize < len(items) {
		items, chunks = items[chunkSize:], append(chunks, items[0:chunkSize:chunkSize])
	}
	return append(chunks, items)
}

func hash(items map[string]string) string {
	var text string
	for k, v := range items {
		text = text + k + v
	}
	algorithm := fnv.New32a()
	_, _ = algorithm.Write([]byte(text))
	return strconv.FormatUint(uint64(algorithm.Sum32()), 10)
}

type GrafanaMsg struct {
	Receiver string  `json:"receiver"`
	Status   string  `json:"status"`
	Alerts   []Alert `json:"alerts"`

	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`

	ExternalURL string `json:"externalURL"`

	Version         string `json:"version"`
	GroupKey        string `json:"groupKey"`
	TruncatedAlerts int    `json:"truncatedAlerts"`
	OrgID           int64  `json:"orgId"`
}

type Alert struct {
	Status        string            `json:"status"`
	Labels        map[string]string `json:"labels"`
	Annotations   map[string]string `json:"annotations"`
	StartsAt      time.Time         `json:"startsAt"`
	EndsAt        time.Time         `json:"endsAt"`
	GeneratorURL  string            `json:"generatorURL"`
	Fingerprint   string            `json:"fingerprint"`
	SilenceURL    string            `json:"silenceURL"`
	DashboardURL  string            `json:"dashboardURL"`
	PanelURL      string            `json:"panelURL"`
	ValueString   string            `json:"valueString"`
	ImageURL      string            `json:"imageURL,omitempty"`
	EmbeddedImage string            `json:"embeddedImage,omitempty"`
}
