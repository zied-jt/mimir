package alertmanager

import (
	"net/url"
	"path/filepath"

	gklog "github.com/go-kit/log"
	"github.com/prometheus/alertmanager/config"
	"github.com/prometheus/alertmanager/notify"
	"github.com/prometheus/alertmanager/template"
	"github.com/prometheus/alertmanager/timeinterval"
	commoncfg "github.com/prometheus/common/config"
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
	cfg, err := config.Load(s)
	if err != nil {
		return nil, err
	}
	return MimirWrapper{conf: cfg}, nil
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
