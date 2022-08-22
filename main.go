package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ory/graceful"
	"github.com/slack-go/slack"
	"golang.org/x/exp/maps"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

var webhookUrl string
var username string

func main() {
	flag.StringVar(&webhookUrl, "webhook-url", "", "Slack webhook url")
	flag.StringVar(&username, "username", "Grafana", "Slack username")
	flag.Parse()

	http.HandleFunc("/slack", handleWebhookRequest)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := graceful.WithDefaults(&http.Server{
		Addr:    ":8080",
		Handler: http.DefaultServeMux,
	})

	log.Println("starting the server")
	if err := graceful.Graceful(server.ListenAndServe, server.Shutdown); err != nil {
		log.Fatalln("failed to gracefully shutdown")
	}
	log.Println("server stopped")
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
	slackMsg := buildMessage(grafanaMsg, channel)

	if err := slack.PostWebhookContext(r.Context(), webhookUrl, &slackMsg); err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func buildMessage(msg GrafanaMsg, channel string) slack.WebhookMessage {
	var blocks []slack.Block

	for i, alert := range msg.Alerts {
		var summary string
		if alert.Status != "resolved" {
			summary = ":sos: " + alert.Annotations["summary"]
		} else {
			summary = ":large_green_circle: " + alert.Annotations["summary"]
		}

		var buttons []slack.BlockElement

		generatorButton := slack.NewButtonBlockElement("generator", "", slack.NewTextBlockObject("plain_text", ":chart_with_upwards_trend: Details", true, false))
		generatorButton.URL = alert.GeneratorURL
		generatorButton.Style = slack.StylePrimary
		buttons = append(buttons, generatorButton)

		if alert.Status != "resolved" {
			if runbookUrl, ok := alert.Annotations["runbook_url"]; ok && runbookUrl != "" {
				runbookButton := slack.NewButtonBlockElement("runbook", "", slack.NewTextBlockObject("plain_text", ":page_with_curl: Runbook", true, false))
				runbookButton.URL = runbookUrl
				runbookButton.Style = slack.StyleDefault
				buttons = append(buttons, runbookButton)
			}
		}

		if alert.Status != "resolved" {
			silenceButton := slack.NewButtonBlockElement("silence", "", slack.NewTextBlockObject("plain_text", ":no_bell: Silence", true, false))
			silenceButton.URL = alert.SilenceURL
			silenceButton.Style = slack.StyleDanger
			buttons = append(buttons, silenceButton)
		}

		labelsNames := maps.Keys(alert.Labels)
		sort.Strings(labelsNames)

		var labelFields []*slack.TextBlockObject
		for _, name := range labelsNames {
			labelFields = append(labelFields, slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*%s*:\n`%s`", name, alert.Labels[name]), false, false))
		}
		chunkedLabelFields := chunkBy(labelFields, 10)

		var labelBlocks []slack.Block
		for _, fields := range chunkedLabelFields {
			labelBlocks = append(labelBlocks, slack.NewSectionBlock(nil, fields, nil))
		}

		var contextElements []slack.MixedElement
		if alert.ValueString != "" {
			contextElements = append(contextElements, slack.NewTextBlockObject("plain_text", fmt.Sprintf("Value: %s", extractValue(alert.ValueString)), true, false))
		}
		contextElements = append(contextElements, slack.NewTextBlockObject("plain_text", fmt.Sprintf("Started at: %s", alert.StartsAt.Format(time.RFC822)), true, false))
		if !alert.EndsAt.IsZero() {
			contextElements = append(contextElements, slack.NewTextBlockObject("plain_text", fmt.Sprintf("Ended at: %s", alert.EndsAt.Format(time.RFC822)), true, false))
		}

		if i != 0 {
			blocks = append(blocks, slack.NewDividerBlock())
		}

		blocks = append(blocks, slack.NewHeaderBlock(slack.NewTextBlockObject("plain_text", summary, true, false)))
		if description, ok := alert.Annotations["description"]; ok && description != "" {
			blocks = append(blocks, slack.NewSectionBlock(slack.NewTextBlockObject("mrkdwn", "> "+description, false, false), nil, nil))
		}
		blocks = append(blocks, labelBlocks...)
		blocks = append(blocks, slack.NewActionBlock("actions", buttons...))
		blocks = append(blocks, slack.NewContextBlock("context", contextElements...))
	}

	return slack.WebhookMessage{
		Username: username,
		Channel:  channel,
		Blocks:   &slack.Blocks{BlockSet: blocks},
	}
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
