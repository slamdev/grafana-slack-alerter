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
	"net/http"
	"sort"
	"strings"
	"time"
)

var webhookUrl string

func main() {
	flag.StringVar(&webhookUrl, "webhook-url", "", "Slack webhook url")
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
	slackMsg := buildMessage(grafanaMsg)

	if err := slack.PostWebhookContext(r.Context(), webhookUrl, &slackMsg); err != nil {
		log.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func buildMessage(msg GrafanaMsg) slack.WebhookMessage {
	for _, alert := range msg.Alerts {
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

		blocks := []slack.Block{
			slack.NewHeaderBlock(slack.NewTextBlockObject("plain_text", summary, true, false)),
			slack.NewSectionBlock(slack.NewTextBlockObject("mrkdwn", "> "+alert.Annotations["description"], false, false), nil, nil),
		}
		blocks = append(blocks, labelBlocks...)
		blocks = append(blocks, slack.NewActionBlock("actions", buttons...))
		blocks = append(blocks, slack.NewContextBlock("context", contextElements...))

		return slack.WebhookMessage{
			Username: "Grafana",
			Channel:  "slamdev-test",
			Blocks:   &slack.Blocks{BlockSet: blocks},
		}
	}
	return slack.WebhookMessage{}
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
	return value[0]
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
