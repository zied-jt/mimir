// SPDX-License-Identifier: AGPL-3.0-only
// This file contains code for the migration from classic mode to UTF-8 strict
// mode in the Alertmanager. It is intended to be used to gather data about
// configurations that are incompatible with the UTF-8 matchers parser, so
// action can be taken to fix those configurations before enabling the mode.

package alertmanager

import (
	"fmt"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/alertmanager/matchers/compat"
	"gopkg.in/yaml.v3"

	"github.com/grafana/mimir/pkg/alertmanager/alertspb"
)

// matchersConfig is a simplified version of an Alertmanager configuration
// containing just the configuration options that are matchers. matchersConfig
// is used to validate that existing configurations are forwards compatible with
// the new UTF-8 parser in Alertmanager (see the matchers/parse package).
type matchersConfig struct {
	Route        *matchersRoute            `yaml:"route,omitempty" json:"route,omitempty"`
	InhibitRules []*matchersInhibitionRule `yaml:"inhibit_rules,omitempty" json:"inhibit_rules,omitempty"`
}

type matchersRoute struct {
	Matchers []string         `yaml:"matchers,omitempty" json:"matchers,omitempty"`
	Routes   []*matchersRoute `yaml:"routes,omitempty" json:"routes,omitempty"`
}

type matchersInhibitionRule struct {
	SourceMatchers []string `yaml:"source_matchers,omitempty" json:"source_matchers,omitempty"`
	TargetMatchers []string `yaml:"target_matchers,omitempty" json:"target_matchers,omitempty"`
}

// checkMatchersInConfigDesc checks if a configuration is forwards compatible with the
// UTF-8 matchers parser (matchers/parse). It does so via loading the same configuration a second
// time but instead via the fallback parser which emits logs and metrics about incompatible inputs
// and disagreement. If no incompatible inputs or disagreement are found, then the Alertmanager
// can be switched to the UTF-8 strict mode. Otherwise, configurations should be fixed before
// enabling the mode.
func checkMatchersInConfigDesc(logger log.Logger, metrics *compat.Metrics, origin string, cfg alertspb.AlertConfigDesc) {
	// Do not add origin to the logger as it's added in the compat package.
	logger = log.With(logger, "user", cfg.User)
	parseFn := compat.FallbackMatchersParser(logger, metrics)
	matchersCfg := matchersConfig{}
	if err := yaml.Unmarshal([]byte(cfg.RawConfig), &matchersCfg); err != nil {
		level.Warn(logger).Log("msg", "Failed to load configuration in checkMatchersInConfigDesc", "origin", origin, "err", err)
		return
	}
	checkRoute(logger, parseFn, origin, matchersCfg.Route, cfg.User)
	checkInhibitionRules(logger, parseFn, origin, matchersCfg.InhibitRules, cfg.User)
}

func checkRoute(logger log.Logger, parseFn compat.ParseMatchers, origin string, r *matchersRoute, user string) {
	if r == nil {
		// This shouldn't be possible, but if somehow a tenant does have a nil route this prevents
		// a nil pointer dereference and a subsequent panic.
		return
	}
	for _, m := range r.Matchers {
		// If parseFn returns an error then the input is invalid in both classic mode and UTF-8
		// strict mode. The alertmanager_matchers_invalid metric will be incremented in the compat
		// package, and all occurrences of disagreement and incompatible inputs will be logged.
		// However, invalid inputs are not logged there, so we log it here just in case we need
		// this information. Though, in general, we are not concerned about inputs that are invalid
		// in both modes.
		if _, err := parseFn(m, origin); err != nil {
			level.Debug(logger).Log("msg", "Invalid matcher in route", "input", m, "origin", origin, "err", err)
		}
	}
	for _, route := range r.Routes {
		checkRoute(logger, parseFn, origin, route, user)
	}
}

func checkInhibitionRules(logger log.Logger, parseFn compat.ParseMatchers, origin string, rules []*matchersInhibitionRule, _ string) {
	for _, r := range rules {
		for _, m := range r.SourceMatchers {
			if _, err := parseFn(m, origin); err != nil {
				// See comments from ValidateRoute.
				level.Debug(logger).Log("msg", "Invalid matcher in inhibition rule source matchers", "input", m, "origin", origin, "err", err)
			}
		}
		for _, m := range r.TargetMatchers {
			if _, err := parseFn(m, origin); err != nil {
				// See comments from ValidateRoute.
				level.Debug(logger).Log("msg", "Invalid matcher in inhibition rule target matchers", "input", m, "origin", origin, "err", err)
			}
		}
	}
}

func enforceMatchersInConfigDesc(logger log.Logger, metrics *compat.Metrics, origin string, cfg alertspb.AlertConfigDesc) error {
	// Do not add origin to the logger as it's added in the compat package.
	logger = log.With(logger, "user", cfg.User)
	parseFn := compat.UTF8MatchersParser(logger, metrics)
	matchersCfg := matchersConfig{}
	if err := yaml.Unmarshal([]byte(cfg.RawConfig), &matchersCfg); err != nil {
		return err
	}
	if err := enforceRoute(logger, parseFn, origin, matchersCfg.Route, cfg.User); err != nil {
		return err
	}
	if err := enforceInhibitionRules(logger, parseFn, origin, matchersCfg.InhibitRules, cfg.User); err != nil {
		return err
	}
	return nil
}

func enforceRoute(logger log.Logger, parseFn compat.ParseMatchers, origin string, r *matchersRoute, user string) error {
	if r == nil {
		// This shouldn't be possible, but if somehow a tenant does have a nil route this prevents
		// a nil pointer dereference and a subsequent panic.
		return nil
	}
	for _, m := range r.Matchers {
		if _, err := parseFn(m, origin); err != nil {
			return fmt.Errorf("Invalid matcher %s in route: %s", m, err)
		}
	}
	for _, route := range r.Routes {
		if err := enforceRoute(logger, parseFn, origin, route, user); err != nil {
			return err
		}
	}
	return nil
}

func enforceInhibitionRules(_ log.Logger, parseFn compat.ParseMatchers, origin string, rules []*matchersInhibitionRule, _ string) error {
	for _, r := range rules {
		for _, m := range r.SourceMatchers {
			if _, err := parseFn(m, origin); err != nil {
				return fmt.Errorf("Invalid matcher %s in inhibition rule source matchers: %s", m, err)
			}
		}
		for _, m := range r.TargetMatchers {
			if _, err := parseFn(m, origin); err != nil {
				return fmt.Errorf("Invalid matcher %s in inhibition rule target matchers: %s", m, err)
			}
		}
	}
	return nil
}
