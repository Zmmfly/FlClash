//go:build webpanel && !android

package core

import (
	"context"
	"net/http"
	"strings"
)

type WebAPIProfileAssetUpload struct {
	WebAPIMutation
	ID      WebAPIID `json:"id"`
	Purpose string   `json:"purpose"`
	Bytes   []byte   `json:"bytes"`
}

type WebAPIProfileAssetResult struct {
	ProfileID WebAPIID                      `json:"profileId"`
	Reference WebAPIStatefulPlanetReference `json:"reference"`
	Content   WebAPIContentSummary          `json:"content"`
}

func webContentPlanetAsset(content WebAPIContent) (WebAPIStatefulPlanetAsset, error) {
	reference := WebAPIStatefulPlanetReference{Purpose: WebAPIStatefulPlanetPurpose, SHA256: strings.TrimPrefix(content.Key.Name, WebAPIStatefulPlanetPurpose+":")}
	if content.Key.Kind != WebAPIContentProfileAsset || content.Key.Name != WebAPIStatefulPlanetPurpose+":"+reference.SHA256 {
		return WebAPIStatefulPlanetAsset{}, webAPIError(400, "invalid_planet_reference", "profile asset requires a purpose-bound planet key")
	}
	if err := webStatefulPlanetReference(reference); err != nil {
		return WebAPIStatefulPlanetAsset{}, err
	}
	return WebAPIStatefulPlanetAsset{ProfileID: content.Key.ID, Reference: reference, Bytes: content.Bytes}, nil
}

func webContentPlanetAssets(contents []WebAPIContent) ([]WebAPIStatefulPlanetAsset, error) {
	assets := make([]WebAPIStatefulPlanetAsset, 0, len(contents))
	for _, content := range contents {
		asset, err := webContentPlanetAsset(content)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	if _, err := WebAPIValidateStatefulPlanetAssets(assets); err != nil {
		return nil, err
	}
	return assets, nil
}

func (s *WebAPIContentService) UploadProfileAsset(ctx context.Context, request WebAPIProfileAssetUpload) (WebAPIReceipt, error) {
	if receipt, done, err := s.preflight(ctx, request.WebAPIMutation, "profiles:upload-asset", request); done || err != nil {
		return receipt, err
	}
	if request.Purpose != WebAPIStatefulPlanetPurpose {
		return WebAPIReceipt{}, webAPIError(400, "invalid_planet_reference", "only purpose-bound ZeroTier planet assets can be uploaded")
	}
	release, err := s.slot(ctx)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	defer release()
	info, err := WebAPIValidateStatefulPlanet(request.Bytes)
	if err != nil {
		return WebAPIReceipt{}, err
	}
	reference := WebAPIStatefulPlanetReference{Purpose: request.Purpose, SHA256: info.SHA256}
	content := WebAPIContent{Key: WebAPIContentKey{Kind: WebAPIContentProfileAsset, ID: request.ID, Name: reference.Purpose + ":" + reference.SHA256}, Bytes: request.Bytes, SHA256: info.SHA256, MediaType: "application/octet-stream"}
	return s.store.Transaction(ctx, request.WebAPIMutation, "profiles:upload-asset", request, func(tx *WebAPIWriteTx) (any, error) {
		if _, err := tx.Profile(request.ID); err != nil {
			return nil, err
		}
		stored, err := tx.ProfileAssets(request.ID)
		if err != nil {
			return nil, err
		}
		found := false
		for _, previous := range stored {
			found = found || previous.Key == content.Key
		}
		if !found {
			stored = append(stored, content)
		}
		if _, err := webContentPlanetAssets(stored); err != nil {
			return nil, err
		}
		if err := tx.PutContent(content); err != nil {
			return nil, err
		}
		return WebAPIProfileAssetResult{ProfileID: request.ID, Reference: reference, Content: webContentSummary(content)}, nil
	})
}

func (s *WebAPIContentService) registerProfileAssets(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/profiles:upload-asset", webContentCommand(WebAPIMaxContentJSONBytes, s.UploadProfileAsset))
}
