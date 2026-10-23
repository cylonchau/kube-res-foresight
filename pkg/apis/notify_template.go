package apis

// NotifyTemplateDTO is the admin-facing representation of one message
// template.
type NotifyTemplateDTO struct {
	Key           string `json:"key"`
	Name          string `json:"name"`
	Channel       string `json:"channel"`
	TitleTemplate string `json:"title_template"`
	BodyTemplate  string `json:"body_template"`
	ButtonsJSON   string `json:"buttons_json"`
	Enabled       bool   `json:"enabled"`
}

// NotifyTemplateUpdateReq updates one template by key (key comes from the
// URL path, not the body — templates are seeded with a fixed set of keys
// and can't be renamed or created/deleted from the admin API).
type NotifyTemplateUpdateReq struct {
	TitleTemplate *string `json:"title_template"`
	BodyTemplate  *string `json:"body_template"`
	ButtonsJSON   *string `json:"buttons_json"`
	Enabled       *bool   `json:"enabled"`
}
