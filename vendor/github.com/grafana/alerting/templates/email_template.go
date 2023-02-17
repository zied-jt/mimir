package templates

import (
	"bytes"
	"embed"
	"html/template"
)

//go:embed emails/*.*
var tmplFS embed.FS

type TemplateConfig struct {
	AppUrl                string
	BuildVersion          string
	BuildStamp            string
	EmailCodeValidMinutes int
}

type EmailTemplate struct {
	cfg  TemplateConfig
	tmpl *template.Template
}

func NewEmailTemplate(cfg TemplateConfig) (*EmailTemplate, error) {
	mailTemplates := template.New("name")
	mailTemplates.Funcs(template.FuncMap{
		"Subject":       subjectTemplateFunc,
		"HiddenSubject": hiddenSubjectTemplateFunc,
	})
	// mailTemplates.Funcs(sprig.FuncMap()) //  Grafana also imports   "github.com/Masterminds/sprig"
	tmpls, err := mailTemplates.ParseFS(tmplFS)
	if err != nil {
		return nil, err
	}
	return &EmailTemplate{
		cfg:  cfg,
		tmpl: tmpls,
	}, nil
}

func (e *EmailTemplate) ExpandEmail(templateName string, data map[string]interface{}) ([]byte, error) {
	if data == nil {
		data = make(map[string]interface{}, 10)
	}
	setDefaultTemplateData(e.cfg, data)
	var buffer bytes.Buffer
	err := e.tmpl.ExecuteTemplate(&buffer, templateName, data)
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// hiddenSubjectTemplateFunc sets the subject template (value) on the map represented by `.Subject.` (obj) so that it can be compiled and executed later.
// It returns a blank string, so there will be no resulting value left in place of the template.
func hiddenSubjectTemplateFunc(obj map[string]interface{}, value string) string {
	obj["value"] = value
	return ""
}

// subjectTemplateFunc does the same thing has hiddenSubjectTemplateFunc, but in addition it executes and returns the subject template using the data represented in `.TemplateData` (data)
// This results in the template being replaced by the subject string.
func subjectTemplateFunc(obj map[string]interface{}, data map[string]interface{}, value string) string {
	obj["value"] = value

	titleTmpl, err := template.New("title").Parse(value)
	if err != nil {
		return ""
	}

	var buf bytes.Buffer
	err = titleTmpl.ExecuteTemplate(&buf, "title", data)
	if err != nil {
		return ""
	}

	subj := buf.String()
	// Since we have already executed the template, save it to subject data so we don't have to do it again later on
	obj["executed_template"] = subj
	return subj
}

func setDefaultTemplateData(cfg TemplateConfig, data map[string]interface{}) {
	// copy from Grafana as is (almost) https://github.com/grafana/grafana/blob/8dab3bf36cd856575965e2e45c9956ef1c0ec2f6/pkg/services/notifications/email.go#L27
	data["AppUrl"] = cfg.AppUrl
	data["BuildVersion"] = cfg.BuildVersion
	data["BuildStamp"] = cfg.BuildStamp
	data["EmailCodeValidHours"] = cfg.EmailCodeValidMinutes / 60
	data["Subject"] = map[string]interface{}{}
	dataCopy := map[string]interface{}{}
	for k, v := range data {
		dataCopy[k] = v
	}
	data["TemplateData"] = dataCopy
}
