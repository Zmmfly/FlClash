//go:build webpanel && !android

package core

type WebAPISettingsSnapshot struct {
	Version          int32                       `json:"version"`
	Runtime          WebAPISettingsRuntime       `json:"runtime"`
	Network          WebAPISettingsNetwork       `json:"network"`
	CurrentProfileID *WebAPIID                   `json:"currentProfileId"`
	Presentation     *WebAPISettingsPresentation `json:"presentation,omitempty"`
	DAV              *WebAPISettingsDAV          `json:"dav,omitempty"`
}

type WebAPISettingsPresentation struct {
	AppSetting        WebAPISettingsAppPresentation `json:"appSetting"`
	ThemeProps        WebAPISettingsTheme           `json:"themeProps"`
	WindowGeometry    WebAPISettingsWindow          `json:"windowGeometry"`
	HotKeyActions     []WebAPISettingsHotKey        `json:"hotKeyActions"`
	ProxiesStyleProps WebAPISettingsProxiesStyle    `json:"proxiesStyleProps"`
}

type WebAPISettingsAppPresentation struct {
	Locale                    *string                       `json:"locale"`
	DashboardWidgets          []string                      `json:"dashboardWidgets"`
	TabAnimation              string                        `json:"tabAnimation"`
	FloatingNavigationBar     bool                          `json:"floatingNavigationBar"`
	SidebarExpanded           bool                          `json:"sidebarExpanded"`
	AcceptedDisclaimerVersion int32                         `json:"acceptedDisclaimerVersion"`
	MinimizeOnExit            bool                          `json:"minimizeOnExit"`
	Hidden                    bool                          `json:"hidden"`
	DeveloperMode             bool                          `json:"developerMode"`
	ShowTrayTitle             bool                          `json:"showTrayTitle"`
	MonochromeTrayIcon        bool                          `json:"monochromeTrayIcon"`
	Filters                   []WebAPISettingsFilter        `json:"filters"`
	HideIP                    bool                          `json:"hideIp"`
	EditorLineWrap            bool                          `json:"editorLineWrap"`
	EditorFontSize            string                        `json:"editorFontSize"`
	ServiceOrder              []string                      `json:"serviceOrder"`
	DisabledServices          []string                      `json:"disabledServices"`
	TableColumnWidths         map[string]map[string]float64 `json:"tableColumnWidths"`
	RecordGroupings           map[string]string             `json:"recordGroupings"`
}

type WebAPISettingsFilter struct {
	Label string `json:"label"`
	Regex string `json:"regex"`
}

type WebAPISettingsTheme struct {
	PrimaryColor     *uint32                 `json:"primaryColor"`
	PrimaryColors    []uint32                `json:"primaryColors"`
	ThemeMode        string                  `json:"themeMode"`
	ColorStyle       string                  `json:"colorStyle"`
	PureBlack        bool                    `json:"pureBlack"`
	SidebarBlur      bool                    `json:"sidebarBlur"`
	LiquidGlass      string                  `json:"liquidGlass"`
	TextScale        WebAPISettingsTextScale `json:"textScale"`
	SystemFontFamily *string                 `json:"systemFontFamily"`
}

type WebAPISettingsTextScale struct {
	Enable bool    `json:"enable"`
	Scale  float64 `json:"scale"`
}

type WebAPISettingsWindow struct {
	Width  float64  `json:"width"`
	Height float64  `json:"height"`
	Top    *float64 `json:"top"`
	Left   *float64 `json:"left"`
	Scale  *float64 `json:"scale"`
}

type WebAPISettingsHotKey struct {
	Action    string   `json:"action"`
	Key       *uint32  `json:"key"`
	Modifiers []string `json:"modifiers"`
}

type WebAPISettingsProxiesStyle struct {
	Type               string `json:"type"`
	SortType           string `json:"sortType"`
	Layout             string `json:"layout"`
	IconStyle          string `json:"iconStyle"`
	CardType           string `json:"cardType"`
	HideTimeoutProxies bool   `json:"hideTimeoutProxies"`
}
