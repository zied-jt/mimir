package receivers

// Base is the base implementation of a notifier. It contains the common fields across all notifier types.
type Base struct {
	Name                  string
	Type                  string
	UID                   string
	DisableResolveMessage bool
}

func (n *Base) GetDisableResolveMessage() bool {
	return n.DisableResolveMessage
}

// NotifierInfo contains metadata of the notifier. Name, UID, Type, etc
type NotifierInfo struct {
	UID                   string
	Name                  string
	Type                  string
	DisableResolveMessage bool
}

func NewBase(cfg NotifierInfo) *Base {
	return &Base{
		UID:                   cfg.UID,
		Name:                  cfg.Name,
		Type:                  cfg.Type,
		DisableResolveMessage: cfg.DisableResolveMessage,
	}
}
