//go:build webpanel && !android

package core

import "errors"

func (tx *WebAPIReadTx) Settings() (WebAPISettings, error) {
	var value WebAPISettings
	err := webAPIGet(tx.tx, "settings", "application", &value)
	var problem *WebAPIError
	if errors.As(err, &problem) && problem.Status == 404 {
		return DefaultWebAPISettings(), nil
	}
	return value, err
}

func (tx *WebAPIWriteTx) SaveSettings(value WebAPISettings) error {
	if value.CurrentProfileID != nil {
		if _, err := tx.Profile(*value.CurrentProfileID); err != nil {
			return webAPIError(409, "invalid_reference", "current profile does not exist")
		}
	}
	return webAPIPut(tx.tx, "settings", "application", value)
}
