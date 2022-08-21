# grafana-slack-alerter

A simple webhook server for [grafana alerts](https://grafana.com/docs/grafana/latest/alerting/) that sends a rich
messages
to slack.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: grafana-slack-alerter
  labels:
    app.kubernetes.io/name: grafana-slack-alerter
spec:
  replicas: 2
  selector:
    matchLabels:
      app.kubernetes.io/name: grafana-slack-alerter
  template:
    metadata:
      labels:
        app.kubernetes.io/name: grafana-slack-alerter
    spec:
      containers:
        - name: grafana-slack-alerter
          image: slamdev/grafana-slack-alerter
          args: [ '--webhook-url=https://hooks.slack.com/services/T0XXX' ]
          ports:
            - name: http
              containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: grafana-slack-alerter
  labels:
    app.kubernetes.io/name: grafana-slack-alerter
spec:
  selector:
    app.kubernetes.io/name: grafana-slack-alerter
  ports:
    - name: http
      port: 80
      targetPort: http
```
