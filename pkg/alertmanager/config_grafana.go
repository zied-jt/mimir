package alertmanager

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"

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

type GrafanaWrapper struct {
	*MimirWrapper
	grafanaTemplates []string
	receiverConfigs  []*notify2.GrafanaReceiverTyped
}

func (g GrafanaWrapper) BuildIntegrationsMap(userID string, tenantDir string, externalURL *url.URL, httpOpts []commoncfg.HTTPClientOption, logger gklog.Logger, notifierWrapper func(string, notify.Notifier) notify.Notifier) (map[string][]notify.Integration, error) {
	integrationMap, err := g.MimirWrapper.BuildIntegrationsMap(userID, tenantDir, externalURL, httpOpts, logger, notifierWrapper)
	if err != nil {
		return nil, err
	}
	if len(g.receiverConfigs) == 0 {
		return integrationMap, nil
	}

	store := &images.UnavailableImageStore{} // TODO Need to figure out what to do with it

	grafanaTmpl, err := buildTemplates(userID, filepath.Join(tenantDir, grafanaTemplatesDir), externalURL, g.grafanaTemplates)
	if err != nil {
		return nil, err
	}

	for _, grafana := range g.receiverConfigs {
		if integrations, ok := integrationMap[grafana.Name]; ok && len(integrations) > 0 {
			return nil, fmt.Errorf("cannot build receiver integrations map, receiver %s declared twice", grafana.Name)
		}

		webhookCli := func(info receivers.NotifierInfo) (receivers.WebhookSender, error) {
			client, err := commoncfg.NewClientFromConfig(*g.conf.Global.HTTPConfig, "grafana-"+info.Type, httpOpts...)
			if err != nil {
				return nil, err
			}
			return &webhookSender{c: client}, nil
		}

		emailCli := func(info receivers.NotifierInfo) (receivers.EmailSender, error) {
			return &emailSender{}, nil
		}

		integrations, err := notify2.BuildReceiverIntegrations(grafana, grafanaTmpl, webhookCli, emailCli, store, func(ctx ...interface{}) logging.Logger {
			return &alertingLogger{
				l: gklog.With(logger, append([]interface{}{"logger"}, ctx...)),
			}
			// TODO change version?!
		}, 1, "v0-beta") // orgID matches HG org ID because there are no orgs.

		if err != nil {
			return nil, fmt.Errorf("failed to build integrations: %w", err)
		}

		integ := make([]notify.Integration, 0, len(integrations))
		for _, integration := range integrations {
			integ = append(integ, *integration)
		}
		integrationMap[grafana.Name] = integ
	}
	return integrationMap, nil
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
	smarthost config.HostPort
	tlsConfig *commoncfg.TLSConfig
	logger    gklog.Logger
	hello     string
}

func (n emailSender) SendEmail(ctx context.Context, cmd *receivers.SendEmailSettings) error {
	return errors.New("NOT IMPLEMENETED YET!") // TODO do something with it
	/*
		var (
			c       *smtp.Client
			conn    net.Conn
			err     error
			success = false
		)
		if n.smarthost.Port == "465" {
			tlsConfig, err := commoncfg.NewTLSConfig(n.tlsConfig)
			if err != nil {
				return errors.Wrap(err, "parse TLS configuration")
			}
			if tlsConfig.ServerName == "" {
				tlsConfig.ServerName = n.smarthost.Host
			}

			conn, err = tls.Dial("tcp", n.smarthost.String(), tlsConfig)
			if err != nil {
				return errors.Wrap(err, "establish TLS connection to server")
			}
		} else {
			var (
				d   = net.Dialer{}
				err error
			)
			conn, err = d.DialContext(ctx, "tcp", n.smarthost.String())
			if err != nil {
				return errors.Wrap(err, "establish connection to server")
			}
		}
		c, err = smtp.NewClient(conn, n.smarthost.Host)
		if err != nil {
			conn.Close()
			return errors.Wrap(err, "create SMTP client")
		}
		defer func() {
			// Try to clean up after ourselves but don't log anything if something has failed.
			if err := c.Quit(); success && err != nil {
				level.Warn(n.logger).Log("msg", "failed to close SMTP connection", "err", err)
			}
		}()

		if n.hello != "" {
			err = c.Hello(n.hello)
			if err != nil {
				return errors.Wrap(err, "send EHLO command")
			}
		}

		// Global Config guarantees RequireTLS is not nil.
		if *n.conf.RequireTLS {
			if ok, _ := c.Extension("STARTTLS"); !ok {
				return true, errors.Errorf("'require_tls' is true (default) but %q does not advertise the STARTTLS extension", n.conf.Smarthost)
			}

			tlsConf, err := commoncfg.NewTLSConfig(&n.conf.TLSConfig)
			if err != nil {
				return false, errors.Wrap(err, "parse TLS configuration")
			}
			if tlsConf.ServerName == "" {
				tlsConf.ServerName = n.conf.Smarthost.Host
			}

			if err := c.StartTLS(tlsConf); err != nil {
				return true, errors.Wrap(err, "send STARTTLS command")
			}
		}

		if ok, mech := c.Extension("AUTH"); ok {
			auth, err := n.auth(mech)
			if err != nil {
				return true, errors.Wrap(err, "find auth mechanism")
			}
			if auth != nil {
				if err := c.Auth(auth); err != nil {
					return true, errors.Wrapf(err, "%T auth", auth)
				}
			}
		}

		var (
			tmplErr error
			data    = notify.GetTemplateData(ctx, n.tmpl, as, n.logger)
			tmpl    = notify.TmplText(n.tmpl, data, &tmplErr)
		)
		from := tmpl(n.conf.From)
		if tmplErr != nil {
			return false, errors.Wrap(tmplErr, "execute 'from' template")
		}
		to := tmpl(n.conf.To)
		if tmplErr != nil {
			return false, errors.Wrap(tmplErr, "execute 'to' template")
		}

		addrs, err := mail.ParseAddressList(from)
		if err != nil {
			return false, errors.Wrap(err, "parse 'from' addresses")
		}
		if len(addrs) != 1 {
			return false, errors.Errorf("must be exactly one 'from' address (got: %d)", len(addrs))
		}
		if err = c.Mail(addrs[0].Address); err != nil {
			return true, errors.Wrap(err, "send MAIL command")
		}
		addrs, err = mail.ParseAddressList(to)
		if err != nil {
			return false, errors.Wrapf(err, "parse 'to' addresses")
		}
		for _, addr := range addrs {
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
		for header, t := range n.conf.Headers {
			value, err := n.tmpl.ExecuteTextString(t, data)
			if err != nil {
				return false, errors.Wrapf(err, "execute %q header template", header)
			}
			fmt.Fprintf(buffer, "%s: %s\r\n", header, mime.QEncoding.Encode("utf-8", value))
		}

		if _, ok := n.conf.Headers["Message-Id"]; !ok {
			fmt.Fprintf(buffer, "Message-Id: %s\r\n", fmt.Sprintf("<%d.%d@%s>", time.Now().UnixNano(), rand.Uint64(), n.hostname))
		}

		multipartBuffer := &bytes.Buffer{}
		multipartWriter := multipart.NewWriter(multipartBuffer)

		fmt.Fprintf(buffer, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
		fmt.Fprintf(buffer, "Content-Type: multipart/alternative;  boundary=%s\r\n", multipartWriter.Boundary())
		fmt.Fprintf(buffer, "MIME-Version: 1.0\r\n\r\n")

		// TODO: Add some useful headers here, such as URL of the alertmanager
		// and active/resolved.
		_, err = message.Write(buffer.Bytes())
		if err != nil {
			return false, errors.Wrap(err, "write headers")
		}

		if len(n.conf.Text) > 0 {
			// Text template
			w, err := multipartWriter.CreatePart(textproto.MIMEHeader{
				"Content-Transfer-Encoding": {"quoted-printable"},
				"Content-Type":              {"text/plain; charset=UTF-8"},
			})
			if err != nil {
				return false, errors.Wrap(err, "create part for text template")
			}
			body, err := n.tmpl.ExecuteTextString(n.conf.Text, data)
			if err != nil {
				return false, errors.Wrap(err, "execute text template")
			}
			qw := quotedprintable.NewWriter(w)
			_, err = qw.Write([]byte(body))
			if err != nil {
				return true, errors.Wrap(err, "write text part")
			}
			err = qw.Close()
			if err != nil {
				return true, errors.Wrap(err, "close text part")
			}
		}

		if len(n.conf.HTML) > 0 {
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
			body, err := n.tmpl.ExecuteHTMLString(n.conf.HTML, data)
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
	*/
}
