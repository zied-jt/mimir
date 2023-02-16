package alertmanager

import (
	"fmt"

	gklog "github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/alerting/images"
	"github.com/grafana/alerting/logging"
	notify2 "github.com/grafana/alerting/notify"
	"github.com/grafana/alerting/receivers"
	"github.com/prometheus/alertmanager/config"
	"github.com/prometheus/alertmanager/notify"
	"github.com/prometheus/alertmanager/template"
	"github.com/prometheus/alertmanager/timeinterval"
	commoncfg "github.com/prometheus/common/config"
)

type GrafanaWrapper struct {
	conf            *config.Config
	receiverConfigs []*notify2.GrafanaReceiverTyped
	webhookSender   func(httpOpts []commoncfg.HTTPClientOption) receivers.WebhookSender
	emailSender     receivers.EmailSender
}

func (g GrafanaWrapper) Templates() []string {
	return g.conf.Templates
}

func (g GrafanaWrapper) InhibitRules() []config.InhibitRule {
	return g.conf.InhibitRules
}

func (g GrafanaWrapper) BuildIntegrationsMap(tmpl *template.Template, httpOps []commoncfg.HTTPClientOption, logger gklog.Logger, notifierWrapper func(string, notify.Notifier) notify.Notifier) (map[string][]notify.Integration, error) {
	store := &images.UnavailableImageStore{} // TODO Need to figure out what to do with it

	upstream, err := buildIntegrationsMap(g.conf.Receivers, tmpl, httpOps, logger, notifierWrapper)
	if err != nil {
		return nil, err
	}

	l := &alertingLogger{l: logger}

	for _, grafana := range g.receiverConfigs {
		if integrations, ok := upstream[grafana.Name]; ok && len(integrations) > 0 {
			return nil, fmt.Errorf("cannot build receiver integrations map, receiver %s declared twice", grafana.Name)
		}

		integrations := notify2.BuildReceiverIntegrations(grafana, tmpl, g.webhookSender(httpOps), g.emailSender, store, func(ctx ...interface{}) logging.Logger {
			return l
		}, 1, "1.0") // TODO orgID and version need to be changed

		integ := make([]notify.Integration, 0, len(integrations))
		for _, integration := range integrations {
			integ = append(integ, *integration)
		}
		upstream[grafana.Name] = integ
	}
	return upstream, nil
}

func (g GrafanaWrapper) TimeIntervals() map[string][]timeinterval.TimeInterval {
	timeIntervals := make(map[string][]timeinterval.TimeInterval, len(g.conf.MuteTimeIntervals)+len(g.conf.TimeIntervals))
	for _, ti := range g.conf.MuteTimeIntervals {
		timeIntervals[ti.Name] = ti.TimeIntervals
	}

	for _, ti := range g.conf.TimeIntervals {
		timeIntervals[ti.Name] = ti.TimeIntervals
	}
	return timeIntervals
}

func (g GrafanaWrapper) Route() *config.Route {
	return g.conf.Route
}

func (g GrafanaWrapper) Raw() *config.Config {
	return g.conf // TODO this is a terrible hack. We need to think what to do with it
}

type alertingLogger struct {
	l gklog.Logger
}

func (l alertingLogger) New(ctx ...interface{}) logging.Logger {
	return &alertingLogger{
		gklog.With(l.l, ctx...),
	}
}

func (l alertingLogger) Log(keyvals ...interface{}) error {
	return l.l.Log(keyvals...)
}

func (l alertingLogger) Debug(msg string, ctx ...interface{}) {
	_ = level.Debug(l.l).Log(append([]interface{}{"msg", msg}, ctx...)...)
}

func (l alertingLogger) Info(msg string, ctx ...interface{}) {
	_ = level.Info(l.l).Log(append([]interface{}{"msg", msg}, ctx...)...)
}

func (l alertingLogger) Warn(msg string, ctx ...interface{}) {
	_ = level.Warn(l.l).Log(append([]interface{}{"msg", msg}, ctx...)...)
}

func (l alertingLogger) Error(msg string, ctx ...interface{}) {
	_ = level.Error(l.l).Log(append([]interface{}{"msg", msg}, ctx...)...)
}
