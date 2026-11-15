//go:build webpanel && !android

package core

import "context"

func (s *WebAPIContentService) BackgroundRefresh(ctx context.Context, key WebAPIContentKey, originalRevision WebAPIRevision, operationID string) (WebAPIReceipt, error) {
	if !WebAPIInternalAuthorized(ctx) || WebAPIRequestSessionID(ctx) != "" {
		return WebAPIReceipt{}, webAPIError(403, "internal_authority_required", "background refresh requires its private internal authority")
	}
	purpose := "source"
	if key.Kind == WebAPIContentMitmResource {
		purpose = "resource"
	}
	return s.Refresh(ctx, WebAPIContentRefresh{WebAPIMutation: WebAPIMutation{ExpectedRevision: originalRevision, OperationID: operationID}, Key: key, Purpose: purpose})
}
