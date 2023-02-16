package alertmanager

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	gklog "github.com/go-kit/log"
	notify2 "github.com/grafana/alerting/notify"
	"github.com/prometheus/alertmanager/config"
	"github.com/prometheus/alertmanager/notify"
	"github.com/prometheus/alertmanager/template"
	"github.com/prometheus/alertmanager/timeinterval"
	commoncfg "github.com/prometheus/common/config"
	"gopkg.in/yaml.v2"
)

// TODO working name... 100% change it
type UserConfigWrapper interface {
	InhibitRules() []config.InhibitRule
	BuildIntegrationsMap(userID string, tenantDir string, externalURL *url.URL, httpOps []commoncfg.HTTPClientOption, logger gklog.Logger, notifierWrapper func(string, notify.Notifier) notify.Notifier) (map[string][]notify.Integration, error)
	TimeIntervals() map[string][]timeinterval.TimeInterval
	Route() *config.Route

	Raw() *config.Config
}

func LoadConfig(s string) (UserConfigWrapper, error) {
	// TODO Copy of the `config.Load` to make unmarshal less strict. Probably change it because Load also populates private field `original` that is used by Coordinator in calculation of a hash that is used in a metric.
	// We do not seem to use the Coordinator, so it should be fine.
	cfg := &config.Config{}
	err := yaml.Unmarshal([]byte(s), cfg)
	if err != nil {
		return nil, err
	}
	// Check if we have a root route. We cannot check for it in the
	// UnmarshalYAML method because it won't be called if the input is empty
	// (e.g. the config file is empty or only contains whitespace).
	if cfg.Route == nil {
		return nil, errors.New("no route provided in config")
	}

	// Check if continue in root route.
	if cfg.Route.Continue {
		return nil, errors.New("cannot have continue in root route")
	}

	if err != nil {
		return nil, err
	}

	mimir := MimirWrapper{conf: cfg}
	// now check if the config has some

	gr := &GrafanaConfig{}
	err = yaml.Unmarshal([]byte(s), gr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Grafana part of rececivers: %w", err)
	}
	typedRecv := make([]notify2.GrafanaReceiverTyped, 0, len(gr.Receivers))
	for _, receiver := range gr.Receivers {
		apiRecv := receiver.ToApiReceiver()
		if apiRecv == nil {
			continue
		}
		// do not really care about the context here because it is used only in the function
		typed, err := notify2.ValidateAPIReceiver(context.TODO(), apiRecv, func(ctx context.Context, sjd map[string][]byte, key string, fallback string) string {
			return fallback
		})
		if err != nil {
			return nil, err
		}
		typedRecv = append(typedRecv, typed)
	}
	if len(typedRecv) == 0 {
		return mimir, nil
	}
	return &GrafanaWrapper{
		MimirWrapper:     &mimir,
		grafanaTemplates: gr.Templates,
		receiverConfigs:  typedRecv,
	}, nil
}

type MimirWrapper struct {
	conf *config.Config
}

func (m MimirWrapper) Raw() *config.Config {
	return m.conf
}

func (m MimirWrapper) Route() *config.Route {
	return m.conf.Route
}

func (m MimirWrapper) InhibitRules() []config.InhibitRule {
	return m.conf.InhibitRules
}

func (m MimirWrapper) BuildIntegrationsMap(userID string, tenantDir string, externalURL *url.URL, httpOps []commoncfg.HTTPClientOption, logger gklog.Logger, notifierWrapper func(string, notify.Notifier) notify.Notifier) (map[string][]notify.Integration, error) {
	tmpl, err := buildTemplates(userID, filepath.Join(tenantDir, templatesDir), externalURL, m.conf.Templates)
	if err != nil {
		return nil, err
	}
	return buildIntegrationsMap(m.conf.Receivers, tmpl, httpOps, logger, notifierWrapper)
}

func (m MimirWrapper) TimeIntervals() map[string][]timeinterval.TimeInterval {
	timeIntervals := make(map[string][]timeinterval.TimeInterval, len(m.conf.MuteTimeIntervals)+len(m.conf.TimeIntervals))
	for _, ti := range m.conf.MuteTimeIntervals {
		timeIntervals[ti.Name] = ti.TimeIntervals
	}

	for _, ti := range m.conf.TimeIntervals {
		timeIntervals[ti.Name] = ti.TimeIntervals
	}
	return timeIntervals
}

func buildTemplates(userID string, dir string, externalURL *url.URL, templates []string) (*template.Template, error) {
	templateFiles := make([]string, len(templates))
	for i, t := range templates {
		templateFilepath, err := safeTemplateFilepath(dir, t)
		if err != nil {
			return nil, err
		}
		templateFiles[i] = templateFilepath
	}
	tmpl, err := template.FromGlobs(templateFiles, withCustomFunctions(userID))

	if err != nil {
		return nil, err
	}
	tmpl.ExternalURL = externalURL
	return tmpl, nil
}
