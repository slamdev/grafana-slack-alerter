package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

func main() {
	http.HandleFunc("/slack", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Println(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		log.Println(string(body))
		msg := WebhookMessage{}
		if err := json.Unmarshal(body, &msg); err != nil {
			log.Println(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		log.Printf("%+v", msg)
		w.WriteHeader(http.StatusOK)
	})
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

type WebhookMessage struct {
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
