package receivers

import "context"

// SendEmailSettings is the command for sending emails
type SendEmailSettings struct {
	To            []string
	SingleEmail   bool
	Template      string
	Subject       string
	Data          map[string]interface{}
	EmbeddedFiles []string
}

type EmailSender interface {
	// SendEmail sends an email. It returns an error if unsuccessful and a flag whether the error is
	// recoverable. This information is useful for a retry logic.
	SendEmail(ctx context.Context, cmd *SendEmailSettings) (bool, error)
}
