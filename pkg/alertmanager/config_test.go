package alertmanager

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfig_GrafanaOnly(t *testing.T) {
	cfg, err := LoadConfig(testDataGrafana)
	require.NoError(t, err)
	require.IsType(t, &GrafanaWrapper{}, cfg)
}

// TODO upstream does not understand `object_matchers`. We need to figure out what to do
const testDataGrafana = `
global:
  http_config:
    follow_redirects: true
    proxy_url: 
    tls_config:
      insecure_skip_verify: false
  opsgenie_api_url: https://api.opsgenie.com/
  pagerduty_url: https://events.pagerduty.com/v2/enqueue
  resolve_timeout: 5m
  slack_api_url: https://hooks.slack.com/services/test
  smtp_from: noreply@grafana.net
  smtp_hello: localhost
  smtp_smarthost: smtprelay:2525
  victorops_api_url: https://alert.victorops.com/integrations/generic/20131114/alert/
  wechat_api_url: https://qyapi.weixin.qq.com/cgi-bin/
route:
  receiver: grafana-default-email
  group_by:
    - grafana_folder
    - alertname
  routes:
    - receiver: grafana-webhook
      matchers:
        - alertname=test
    - continue: true
      group_by: []
      matchers: []
      mute_time_intervals: []
      receiver: slack-notifications
      routes: []
templates:
  - test-template
receivers:
  - name: slack-notifications
    slack_configs:
      - channel: '#alerting'
        send_resolved: true
  - name: grafana-default-email
    grafana_managed_receiver_configs:
      - uid: ecdf4ce4-4dad-4f71-bfed-f871c7bc1d8a
        name: email receiver
        type: email
        disableResolveMessage: false
        settings:
          addresses: <example@email.com>
  - name: grafana-webhook
    grafana_managed_receiver_configs:
      - uid: b6980932-d686-4d62-bef1-084ba5eb6fcb
        name: grafana-webhook
        type: webhook
        disableResolveMessage: false
        settings:
          url: http://webhooks.com
`
