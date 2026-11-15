//go:build webpanel && !android

package core

type WebAPISettingsMigrationValueKind string

type WebAPISettingsMigrationValue struct {
	Kind         WebAPISettingsMigrationValueKind `json:"kind"`
	Bool         *bool                            `json:"bool,omitempty"`
	String       *string                          `json:"string,omitempty"`
	Integer      *int32                           `json:"integer,omitempty"`
	Strings      []string                         `json:"strings"`
	Window       *WebAPISettingsWindow            `json:"window,omitempty"`
	WindowFields []string                         `json:"windowFields,omitempty"`
	NTP          *WebAPINTP                       `json:"ntp,omitempty"`
}

type WebAPISettingsMigrationChange struct {
	SourceField          string                        `json:"sourceField"`
	TargetField          string                        `json:"targetField,omitempty"`
	Action               string                        `json:"action"`
	Before               WebAPISettingsMigrationValue  `json:"before"`
	After                *WebAPISettingsMigrationValue `json:"after"`
	Lossless             bool                          `json:"lossless"`
	RequiresConfirmation bool                          `json:"requiresConfirmation"`
}

func webAPISettingsMigrationString(value string) WebAPISettingsMigrationValue {
	return WebAPISettingsMigrationValue{Kind: "string", String: &value}
}

func webAPISettingsMigrationBool(value bool) WebAPISettingsMigrationValue {
	return WebAPISettingsMigrationValue{Kind: "bool", Bool: &value}
}

func webAPISettingsMigrationInteger(value int32) WebAPISettingsMigrationValue {
	return WebAPISettingsMigrationValue{Kind: "integer", Integer: &value}
}

func webAPISettingsMigrationStrings(value []string) WebAPISettingsMigrationValue {
	return WebAPISettingsMigrationValue{Kind: "strings", Strings: append([]string{}, value...)}
}

func webAPISettingsMigrationWindow(value WebAPISettingsWindow) WebAPISettingsMigrationValue {
	clone := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		copy := *v
		return &copy
	}
	value.Top = clone(value.Top)
	value.Left = clone(value.Left)
	value.Scale = clone(value.Scale)
	return WebAPISettingsMigrationValue{Kind: "window", Window: &value}
}

func (plan *WebAPISettingsMigrationPlan) change(source, target string, before WebAPISettingsMigrationValue, after *WebAPISettingsMigrationValue, lossless bool) {
	action := "replace"
	if after == nil {
		action = "delete"
	} else if lossless && source != target {
		action = "rename"
	}
	confirmation := !lossless
	plan.Changes = append(plan.Changes, WebAPISettingsMigrationChange{SourceField: source, TargetField: target, Action: action, Before: before, After: after, Lossless: lossless, RequiresConfirmation: confirmation})
	if confirmation {
		plan.RequiresConfirmation = true
	}
}

func (plan *WebAPISettingsMigrationPlan) displayString(source, target, before, after string, lossless bool) {
	value := webAPISettingsMigrationString(after)
	plan.change(source, target, webAPISettingsMigrationString(before), &value, lossless)
}

func webAPISettingsMigrationNTP(value WebAPINTP) WebAPISettingsMigrationValue {
	return WebAPISettingsMigrationValue{Kind: "ntp", NTP: &value}
}

func webAPISettingsMigrationNull() WebAPISettingsMigrationValue {
	return WebAPISettingsMigrationValue{Kind: "null"}
}
