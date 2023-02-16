package notify

import (
	"fmt"

	"github.com/prometheus/alertmanager/notify"
	"github.com/prometheus/alertmanager/template"

	"github.com/grafana/alerting/images"
	"github.com/grafana/alerting/logging"
	"github.com/grafana/alerting/receivers"
	"github.com/grafana/alerting/receivers/alertmanager"
	"github.com/grafana/alerting/receivers/dinding"
	"github.com/grafana/alerting/receivers/discord"
	"github.com/grafana/alerting/receivers/email"
	"github.com/grafana/alerting/receivers/googlechat"
	"github.com/grafana/alerting/receivers/kafka"
	"github.com/grafana/alerting/receivers/line"
	"github.com/grafana/alerting/receivers/opsgenie"
	"github.com/grafana/alerting/receivers/pagerduty"
	"github.com/grafana/alerting/receivers/pushover"
	"github.com/grafana/alerting/receivers/sensugo"
	"github.com/grafana/alerting/receivers/slack"
	"github.com/grafana/alerting/receivers/teams"
	"github.com/grafana/alerting/receivers/telegram"
	"github.com/grafana/alerting/receivers/threema"
	"github.com/grafana/alerting/receivers/victorops"
	"github.com/grafana/alerting/receivers/webex"
	"github.com/grafana/alerting/receivers/webhook"
	"github.com/grafana/alerting/receivers/wecom"
)

func BuildReceiverIntegrations(
	receiver *GrafanaReceiverTyped,
	tmpl *template.Template,
	ns func(n receivers.NotifierInfo) (receivers.WebhookSender, error),
	es func(n receivers.NotifierInfo) (receivers.EmailSender, error),
	img images.ImageStore, // Used by some receivers to include as part of the source
	logFactory logging.LoggerFactory,

	orgID int64,
	version string,
) ([]*Integration, error) {
	var integrations []*Integration

	type notificationChannel interface {
		notify.Notifier
		notify.ResolvedSender
	}

	var err error

	createIntegration := func(idx int, cfg receivers.NotifierInfo, f func(logger logging.Logger) notificationChannel) {
		if err != nil { // if there is at least one error, just skip all remaining calls. this is to simplify the logic below
			return
		}
		logger := logFactory("ngalert.notifier." + cfg.Type) // TODO add more context here
		n := f(logger)
		i := NewIntegration(n, n, cfg.Type, idx)
		integrations = append(integrations, i)
	}

	createIntegrationWithWebhook := func(idx int, cfg receivers.NotifierInfo, f func(logger logging.Logger, w receivers.WebhookSender) notificationChannel) {
		if err != nil { // if there is at least one error, just skip all remaining calls. this is to simplify the logic below
			return
		}
		w, e := ns(cfg)
		if e != nil {
			err = fmt.Errorf("unable to build webhook client for %s notifier %s (UID: %s): %w ", cfg.Type, cfg.Name, cfg.UID, e)
		}
		createIntegration(idx, cfg, func(logger logging.Logger) notificationChannel {
			return f(logger, w)
		})
	}

	for i, cfg := range receiver.AlertmanagerConfigs {
		createIntegration(i, cfg.NotifierInfo, func(l logging.Logger) notificationChannel {
			return alertmanager.New(cfg.Settings, cfg.NotifierInfo, img, l)
		})
	}
	for i, cfg := range receiver.DingdingConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return dinding.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, l)
		})
	}
	for i, cfg := range receiver.DiscordConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return discord.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l, version)
		})
	}
	for i, cfg := range receiver.EmailConfigs {
		mailCli, e := es(cfg.NotifierInfo)
		if e != nil {
			err = fmt.Errorf("unable to build webhook client for %s notifier %s (UID: %s): %w ", cfg.Type, cfg.Name, cfg.UID, e)
			break
		}
		createIntegration(i, cfg.NotifierInfo, func(l logging.Logger) notificationChannel {
			return email.New(cfg.Settings, cfg.NotifierInfo, tmpl, mailCli, img, l)
		})
	}
	for i, cfg := range receiver.GooglechatConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return googlechat.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l, version)
		})
	}
	for i, cfg := range receiver.KafkaConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return kafka.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l)
		})
	}
	for i, cfg := range receiver.LineConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return line.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, l)
		})
	}
	for i, cfg := range receiver.OpsgenieConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return opsgenie.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l)
		})
	}
	for i, cfg := range receiver.PagerdutyConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return pagerduty.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l)
		})
	}
	for i, cfg := range receiver.PushoverConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return pushover.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l)
		})
	}
	for i, cfg := range receiver.SensugoConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return sensugo.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l)
		})
	}
	for i, cfg := range receiver.SlackConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return slack.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l, version)
		})
	}
	for i, cfg := range receiver.TeamsConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return teams.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l)
		})
	}
	for i, cfg := range receiver.TelegramConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return telegram.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l)
		})
	}
	for i, cfg := range receiver.ThreemaConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return threema.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l)
		})
	}
	for i, cfg := range receiver.VictoropsConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return victorops.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, img, l, version)
		})
	}
	for i, cfg := range receiver.WebhookConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return webhook.New(cfg.Settings, orgID, cfg.NotifierInfo, tmpl, w, img, l)
		})
	}
	for i, cfg := range receiver.WecomConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return wecom.New(cfg.Settings, cfg.NotifierInfo, tmpl, w, l)
		})
	}
	for i, cfg := range receiver.WebexConfigs {
		createIntegrationWithWebhook(i, cfg.NotifierInfo, func(l logging.Logger, w receivers.WebhookSender) notificationChannel {
			return webex.New(cfg.Settings, orgID, cfg.NotifierInfo, tmpl, w, img, l)
		})
	}

	if err != nil {
		return nil, err
	}
	return integrations, nil
}
