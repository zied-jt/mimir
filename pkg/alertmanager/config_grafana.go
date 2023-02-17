package alertmanager

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/quotedprintable"
	"net/textproto"

	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"path/filepath"
	"time"

	"github.com/grafana/alerting/templates"
	"github.com/pkg/errors"

	gklog "github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/alerting/images"
	"github.com/grafana/alerting/logging"
	notify2 "github.com/grafana/alerting/notify"
	"github.com/grafana/alerting/receivers"
	"github.com/prometheus/alertmanager/config"
	"github.com/prometheus/alertmanager/notify"
	commoncfg "github.com/prometheus/common/config"
)

const (
	grafanaTemplatesDir = "temlates_grafana"
)

type GrafanaConfig struct {
	Templates []string           `yaml:"grafana_templates,omitempty"`
	Receivers []GrafanaReceivers `yaml:"receivers,omitempty" json:"receivers,omitempty"`
}

type GrafanaReceivers struct {
	// A unique identifier for this receiver.
	Name      string             `yaml:"name" json:"name"`
	Receivers []*GrafanaReceiver `yaml:"grafana_managed_receiver_configs,omitempty" json:"grafana_managed_receiver_configs,omitempty"`
}

func (g GrafanaReceivers) ToApiReceiver() *notify2.APIReceiver {
	recv := make([]*notify2.GrafanaReceiver, 0, len(g.Receivers))
	for _, receiver := range g.Receivers {
		recv = append(recv, &notify2.GrafanaReceiver{
			UID:                   receiver.UID,
			Name:                  receiver.Name,
			Type:                  receiver.Type,
			DisableResolveMessage: receiver.DisableResolveMessage,
			Settings:              json.RawMessage(receiver.Settings),
			SecureSettings:        nil,
		})
	}
	if len(recv) == 0 {
		return nil
	}
	return &notify2.APIReceiver{
		ConfigReceiver: config.Receiver{Name: g.Name},
		GrafanaReceivers: notify2.GrafanaReceivers{
			Receivers: recv,
		},
	}
}

type GrafanaReceiver struct {
	UID                   string     `yaml:"uid"`
	Name                  string     `yaml:"name"`
	Type                  string     `yaml:"type"`
	DisableResolveMessage bool       `yaml:"disableResolveMessage"`
	Settings              RawMessage `yaml:"settings"` // TODO figure something out... this is a terrible hack
}

type RawMessage json.RawMessage // This type alias adds YAML marshaling to the json.RawMessage.

// MarshalJSON returns m as the JSON encoding of m.
func (r RawMessage) MarshalJSON() ([]byte, error) {
	return json.Marshal(json.RawMessage(r))
}

func (r *RawMessage) UnmarshalJSON(data []byte) error {
	var raw json.RawMessage
	err := json.Unmarshal(data, &raw)
	if err != nil {
		return err
	}
	*r = RawMessage(raw)
	return nil
}

func (r *RawMessage) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var data map[string]interface{}
	if err := unmarshal(&data); err != nil {
		return err
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return err
	}
	*r = bytes
	return nil
}

func (r RawMessage) MarshalYAML() (interface{}, error) {
	if r == nil {
		return nil, nil
	}
	var d interface{}
	err := json.Unmarshal(r, &d)
	if err != nil {
		return nil, err
	}
	return d, nil
}

type GrafanaWrapper struct {
	*MimirWrapper
	grafanaTemplates []string
	receiverConfigs  []notify2.GrafanaReceiverTyped
}

func (g GrafanaWrapper) BuildIntegrationsMap(userID string, tenantDir string, externalURL *url.URL, httpOpts []commoncfg.HTTPClientOption, logger gklog.Logger, notifierWrapper func(string, notify.Notifier) notify.Notifier) ([]*notify.Receiver, error) {
	integrations, err := g.MimirWrapper.BuildIntegrationsMap(userID, tenantDir, externalURL, httpOpts, logger, notifierWrapper)
	if err != nil {
		return nil, err
	}
	if len(g.receiverConfigs) == 0 {
		return integrations, nil
	}

	store := &images.UnavailableImageStore{} // TODO Need to figure out what to do with it

	integrationMap := make(map[string]*notify.Receiver, len(integrations))
	for _, integration := range integrations {
		integrationMap[integration.Name()] = integration
	}

	grafanaTmpl, err := buildTemplates(userID, filepath.Join(tenantDir, grafanaTemplatesDir), externalURL, append(g.grafanaTemplates, "__default__.tmpl"))
	if err != nil {
		return nil, err
	}

	for _, grafana := range g.receiverConfigs {
		if recv, ok := integrationMap[grafana.Name]; ok && len(recv.Integrations()) > 0 { // TODO Probably we can mix... shouldn't be a problem. Leave unmixed for now
			return nil, fmt.Errorf("cannot build receiver integrations map, receiver %s declared twice", grafana.Name)
		}

		webhookCli := func(info receivers.NotifierInfo) (receivers.WebhookSender, error) {
			client, err := commoncfg.NewClientFromConfig(*g.conf.Global.HTTPConfig, "grafana-"+info.Type, httpOpts...)
			if err != nil {
				return nil, err
			}
			// TODO create logger with notifier info in the context
			return &webhookSender{c: client, l: logger}, nil
		}

		emailCli := func(info receivers.NotifierInfo) (receivers.EmailSender, error) {
			emailTemplate, err := templates.NewEmailTemplate(templates.TemplateConfig{
				AppUrl:                externalURL.String(),
				BuildVersion:          "0.0.0-alpha",
				BuildStamp:            "n/a",
				EmailCodeValidMinutes: 0,
			})
			if err != nil {
				return nil, errors.Wrap(err, "fail to initialize email templates")
			}
			return &emailSender{
				conf:   g.conf.Global,
				tmpl:   emailTemplate,
				logger: logger,
			}, nil
		}

		integr, err := notify2.BuildReceiverIntegrations(grafana, grafanaTmpl, webhookCli, emailCli, store, func(ctx ...interface{}) logging.Logger {
			return &alertingLogger{
				l: gklog.With(logger, append([]interface{}{"logger"}, ctx...)),
			}
			// TODO change version?!
		}, 1, "v0-beta") // orgID matches HG org ID because there are no orgs.

		if err != nil {
			return nil, fmt.Errorf("failed to build integrations: %w", err)
		}

		integrationMap[grafana.Name] = notify.NewReceiver(grafana.Name, true, integr)
	}

	result := make([]*notify.Receiver, 0, len(integrationMap))
	for _, receiver := range integrationMap {
		result = append(result, receiver)
	}
	return result, nil
}

func (g GrafanaWrapper) Raw() *config.Config {
	return g.MimirWrapper.Raw() // TODO this is a terrible hack because API accepts only Alertmanager config. We need to think what to do with it
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

type webhookSender struct {
	c *http.Client
	l gklog.Logger
}

// this is 1:1 copy from Grafana webhook notifier
func (w webhookSender) SendWebhook(ctx context.Context, cmd *receivers.SendWebhookSettings) error {
	if cmd.HTTPMethod == "" {
		cmd.HTTPMethod = http.MethodPost
	}

	level.Debug(w.l).Log("msg", "Sending webhook", "url", cmd.URL, "http method", cmd.HTTPMethod)

	if cmd.HTTPMethod != http.MethodPost && cmd.HTTPMethod != http.MethodPut {
		return fmt.Errorf("webhook only supports HTTP methods PUT or POST")
	}

	request, err := http.NewRequestWithContext(ctx, cmd.HTTPMethod, cmd.URL, bytes.NewReader([]byte(cmd.Body)))
	if err != nil {
		return err
	}

	if cmd.ContentType == "" {
		cmd.ContentType = "application/json"
	}

	request.Header.Set("Content-Type", cmd.ContentType)
	request.Header.Set("User-Agent", "Grafana")

	if cmd.User != "" && cmd.Password != "" {
		request.Header.Set("Authorization", base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", cmd.User, cmd.Password))))
	}

	for k, v := range cmd.HTTPHeader {
		request.Header.Set(k, v)
	}

	resp, err := w.c.Do(request)
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			level.Warn(w.l).Log("msg", "Failed to close response body", "err", err)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if cmd.Validation != nil {
		err := cmd.Validation(body, resp.StatusCode)
		if err != nil {
			level.Debug(w.l).Log("msg", "Webhook failed validation", "url", cmd.URL, "statuscode", resp.Status, "body", string(body))
			return fmt.Errorf("webhook failed validation: %w", err)
		}
	}

	if resp.StatusCode/100 == 2 {
		level.Debug(w.l).Log("Webhook succeeded", "url", cmd.URL, "statuscode", resp.Status)
		return nil
	}

	level.Debug(w.l).Log("msg", "Webhook failed", "url", cmd.URL, "statuscode", resp.Status, "body", string(body))
	return fmt.Errorf("webhook response status %v", resp.Status)
}

type emailSender struct {
	conf   *config.GlobalConfig
	tmpl   *templates.EmailTemplate
	logger gklog.Logger
}

func (n emailSender) SendEmail(ctx context.Context, cmd *receivers.SendEmailSettings) (bool, error) {
	var (
		c       *smtp.Client
		conn    net.Conn
		err     error
		success = false
		d       = net.Dialer{}
	)

	conn, err = d.DialContext(ctx, "tcp", n.conf.SMTPSmarthost.String())
	if err != nil {
		return true, errors.Wrap(err, "establish connection to server")
	}

	c, err = smtp.NewClient(conn, n.conf.SMTPSmarthost.Host)
	if err != nil {
		conn.Close()
		return true, errors.Wrap(err, "create SMTP client")
	}
	defer func() {
		// Try to clean up after ourselves but don't log anything if something has failed.
		if err := c.Quit(); success && err != nil {
			level.Warn(n.logger).Log("msg", "failed to close SMTP connection", "err", err)
		}
	}()

	if n.conf.SMTPHello != "" {
		err = c.Hello(n.conf.SMTPHello)
		if err != nil {
			return true, errors.Wrap(err, "send EHLO command")
		}
	}

	// Skipping TLS for now
	// // Global Config guarantees RequireTLS is not nil.
	// if n.conf.SMTPRequireTLS {
	// 	if ok, _ := c.Extension("STARTTLS"); !ok {
	// 		return true, errors.Errorf("'require_tls' is true (default) but %q does not advertise the STARTTLS extension", n.conf.SMTPSmarthost)
	// 	}
	//
	// 	tlsConf, err := commoncfg.NewTLSConfig(n.tlsConfig)
	// 	if err != nil {
	// 		return false, errors.Wrap(err, "parse TLS configuration")
	// 	}
	// 	if tlsConf.ServerName == "" {
	// 		tlsConf.ServerName = n.conf.SMTPSmarthost.Host
	// 	}
	//
	// 	if err := c.StartTLS(tlsConf); err != nil {
	// 		return true, errors.Wrap(err, "send STARTTLS command")
	// 	}
	// }

	// TODO skipping auth for now as we do not need it

	// if ok, mech := c.Extension("AUTH"); ok {
	// 	auth, err := n.auth(mech)
	// 	if err != nil {
	// 		return true, errors.Wrap(err, "find auth mechanism")
	// 	}
	// 	if auth != nil {
	// 		if err := c.Auth(auth); err != nil {
	// 			return true, errors.Wrapf(err, "%T auth", auth)
	// 		}
	// 	}
	// }

	addrs, err := mail.ParseAddressList(n.conf.SMTPFrom) // TODO in global config this can be a template!
	if err != nil {
		return false, errors.Wrap(err, "parse 'from' addresses")
	}
	if len(addrs) != 1 {
		return false, errors.Errorf("must be exactly one 'from' address (got: %d)", len(addrs))
	}
	if err = c.Mail(addrs[0].Address); err != nil {
		return true, errors.Wrap(err, "send MAIL command")
	}
	for _, a := range cmd.To {
		addr, err := mail.ParseAddress(a)
		if err != nil {
			return false, errors.Wrapf(err, "parse 'to' addresses")
		}
		if err = c.Rcpt(addr.Address); err != nil {
			return true, errors.Wrapf(err, "send RCPT command")
		}
	}

	// Send the email headers and body.
	message, err := c.Data()
	if err != nil {
		return true, errors.Wrapf(err, "send DATA command")
	}
	defer message.Close()

	buffer := &bytes.Buffer{}
	// No headers for now

	// for header, t := range n.conf.Headers {
	// 	value, err := n.tmpl.ExecuteTextString(t, data)
	// 	if err != nil {
	// 		return false, errors.Wrapf(err, "execute %q header template", header)
	// 	}
	// 	fmt.Fprintf(buffer, "%s: %s\r\n", header, mime.QEncoding.Encode("utf-8", value))
	// }
	//
	// if _, ok := n.conf.Headers["Message-Id"]; !ok {
	// 	fmt.Fprintf(buffer, "Message-Id: %s\r\n", fmt.Sprintf("<%d.%d@%s>", time.Now().UnixNano(), rand.Uint64(), n.hostname))
	// }

	multipartBuffer := &bytes.Buffer{}
	multipartWriter := multipart.NewWriter(multipartBuffer)

	fmt.Fprintf(buffer, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(buffer, "Content-Type: multipart/alternative;  boundary=%s\r\n", multipartWriter.Boundary())
	fmt.Fprintf(buffer, "MIME-Version: 1.0\r\n\r\n")

	_, err = message.Write(buffer.Bytes())
	if err != nil {
		return false, errors.Wrap(err, "write headers")
	}

	// Html template
	// Preferred alternative placed last per section 5.1.4 of RFC 2046
	// https://www.ietf.org/rfc/rfc2046.txt
	w, err := multipartWriter.CreatePart(textproto.MIMEHeader{
		"Content-Transfer-Encoding": {"quoted-printable"},
		"Content-Type":              {"text/html; charset=UTF-8"},
	})
	if err != nil {
		return false, errors.Wrap(err, "create part for html template")
	}
	body, err := n.tmpl.ExpandEmail(cmd.Template+".html", cmd.Data)
	if err != nil {
		return false, errors.Wrap(err, "execute html template")
	}
	qw := quotedprintable.NewWriter(w)
	_, err = qw.Write([]byte(body))
	if err != nil {
		return true, errors.Wrap(err, "write HTML part")
	}
	err = qw.Close()
	if err != nil {
		return true, errors.Wrap(err, "close HTML part")
	}

	err = multipartWriter.Close()
	if err != nil {
		return false, errors.Wrap(err, "close multipartWriter")
	}

	_, err = message.Write(multipartBuffer.Bytes())
	if err != nil {
		return false, errors.Wrap(err, "write body buffer")
	}

	success = true
	return false, nil
}
