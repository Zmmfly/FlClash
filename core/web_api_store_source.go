//go:build webpanel && !android

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/netip"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	bolt "github.com/metacubex/bbolt"
)

var webAPISourceBuckets = []string{"source-grants", "source-receipts", "source-operation-tombstones"}

type WebAPISourceAuthorizationTx struct {
	*WebAPIReadTx
	sessionID        string
	expectedRevision WebAPIRevision
	urlDigest        string
}

type webAPIStoredSourceReceipt struct {
	Revision    WebAPIRevision  `json:"revision"`
	Hash        string          `json:"hash"`
	SessionID   string          `json:"sessionId"`
	OperationID string          `json:"operationId"`
	Body        json.RawMessage `json:"body"`
}

func webAPISourceOpaqueKey(value string) bool {
	return value != "" && len(value) <= 1024 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func (tx *WebAPIReadTx) SourceGrant(id string) (WebAPISourceGrant, error) {
	var grant WebAPISourceGrant
	if !webAPISourceOpaqueKey(id) {
		return grant, webAPIError(400, "invalid_source_grant", "invalid source authorization identity")
	}
	err := webAPIGet(tx.tx, "source-grants", id, &grant)
	return grant, err
}

func (tx *WebAPIReadTx) SourceGrantReceipt(operationID string) (WebAPISourceAuthorizationReceipt, error) {
	if !webAPIOperationID(operationID) {
		return WebAPISourceAuthorizationReceipt{}, webAPIError(400, "invalid_operation_id", "invalid operationId")
	}
	return webAPIReadSourceReceipt(tx.tx, operationID, "")
}

func (s *WebAPIStore) SourceGrant(ctx context.Context, id string) (WebAPISourceGrant, error) {
	var grant WebAPISourceGrant
	sessionID := WebAPIRequestSessionID(ctx)
	if sessionID == "" {
		return grant, webAPIError(401, "unauthorized", "source authorization requires an authenticated session")
	}
	_, err := s.View(ctx, func(tx *WebAPIReadTx) error {
		release, err := WebAPIAcquireCommitAuthorization(ctx)
		if err != nil {
			return err
		}
		defer release()
		grant, err = tx.SourceGrant(id)
		if err != nil {
			return err
		}
		if grant.SessionID != sessionID {
			return webAPIError(403, "source_authorization_mismatch", "source authorization belongs to another session")
		}
		return nil
	})
	if err != nil {
		return WebAPISourceGrant{}, err
	}
	return grant, nil
}

func (s *WebAPIStore) SourceGrantReceipt(ctx context.Context, operationID string) (WebAPISourceAuthorizationReceipt, error) {
	var result WebAPISourceAuthorizationReceipt
	if !webAPIOperationID(operationID) {
		return result, webAPIError(400, "invalid_operation_id", "invalid operationId")
	}
	sessionID := WebAPIRequestSessionID(ctx)
	if sessionID == "" {
		return result, webAPIError(401, "unauthorized", "source authorization requires an authenticated session")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	err := s.read(func(tx *bolt.Tx) error {
		release, err := WebAPIAcquireCommitAuthorization(ctx)
		if err != nil {
			return err
		}
		defer release()
		result, err = webAPIReadSourceReceipt(tx, operationID, sessionID)
		return err
	})
	return result, err
}

func (tx *WebAPISourceAuthorizationTx) SaveSourceGrant(grant WebAPISourceGrant) error {
	if !webAPISourceOpaqueKey(grant.ID) || !webAPISourceOpaqueKey(grant.SessionID) || grant.SessionID != tx.sessionID {
		return webAPIError(400, "invalid_source_grant", "invalid source authorization identity or session binding")
	}
	if !webAPISourceGrantPurpose(grant.Key, grant.Purpose) {
		return webAPIError(400, "invalid_source_grant", "source purpose does not match its resource")
	}
	importing := webSourceImportPurpose(grant.Purpose) || webAPICustomImportPurpose(grant.Purpose)
	if importing {
		if !webAPIOperationID(grant.ImportJobID) {
			return webAPIError(400, "invalid_source_grant", "source import requires an immutable job binding")
		}
		if grant.Purpose == WebAPISourceMitmResourceImport || grant.Purpose == "custom-profile-dependency" {
			if !webAPISourceOpaqueKey(grant.SourceReference) || !webAPISourceDigest(grant.RequestDigest) {
				return webAPIError(400, "invalid_source_grant", "pending dependency requires source and request digest bindings")
			}
		} else if grant.Purpose == "custom-profile-import" {
			if grant.SourceReference != "" || grant.RequestDigest != "" && !webAPISourceDigest(grant.RequestDigest) {
				return webAPIError(400, "invalid_source_grant", "invalid custom profile import bindings")
			}
		} else if grant.SourceReference != "" || grant.RequestDigest != "" {
			return webAPIError(400, "invalid_source_grant", "this import purpose does not accept a dependency reference")
		}
	} else {
		if grant.ImportJobID != "" || grant.SourceReference != "" {
			return webAPIError(400, "invalid_source_grant", "refresh authorizations cannot carry prospective import bindings")
		}
		digestDomain := grant.Key != nil && webAPIContains([]string{"dav", "icon-image", "geo", "external-provider"}, grant.Key.Kind)
		if grant.RequestDigest != "" && (!digestDomain || !webAPISourceDigest(grant.RequestDigest)) {
			return webAPIError(400, "invalid_source_grant", "invalid source domain request digest")
		}
		if grant.Key != nil && grant.Key.Kind == "dav" && !webAPISourceDigest(grant.RequestDigest) {
			return webAPIError(400, "invalid_source_grant", "DAV authorization requires the full tuple digest")
		}
	}
	if grant.Key != nil && (!webAPISourceOpaqueKey(grant.Key.Kind) || !webAPISourceOpaqueKey(grant.Key.ID) || len(grant.Key.Name) > 1024 || !utf8.ValidString(grant.Key.Name) || strings.ContainsAny(grant.Key.Name, "\x00\r\n")) {
		return webAPIError(400, "invalid_source_grant", "invalid source resource binding")
	}
	if grant.Key != nil && grant.Key.Kind != "mitm-resource" && grant.Key.Kind != "custom-import-dependency" && grant.Key.Name != "" {
		return webAPIError(400, "invalid_source_grant", "source resource has no named child")
	}

	if grant.Key != nil {
		bucket := webAPISourceResourceBucket(grant.Key.Kind)
		if bucket == "" {
			valid := false
			switch grant.Key.Kind {
			case "dav":
				valid = grant.Key.ID == "settings"
			case "icon-image":
				valid = webAPISourceUint256Decimal(grant.Key.ID, true)
			case "geo", "external-provider":
				valid = webAPISourceUint256Decimal(grant.Key.ID, false)
			}
			if !valid {
				return webAPIError(400, "invalid_source_grant", "invalid source resource kind or identity")
			}
		}
		if bucket != "" {
			if _, err := webAPIInteger(grant.Key.ID, true); err != nil {
				return webAPIError(400, "invalid_source_grant", "invalid source resource identity")
			}
			if tx.tx.Bucket([]byte(bucket)).Get([]byte(grant.Key.ID)) == nil {
				return webAPIError(409, "invalid_reference", "source authorization resource does not exist")
			}
		}
	}
	if webAPICustomImportPurpose(grant.Purpose) {
		var owner struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if err := webAPIGet(tx.tx, "profiles", grant.Key.ID, &owner); err != nil || owner.ID != grant.Key.ID || owner.Type != "custom" {
			return webAPIError(409, "invalid_reference", "custom import requires an existing custom profile owner")
		}
		if grant.Purpose == "custom-profile-dependency" && grant.SourceReference != grant.Key.Name {
			return webAPIError(400, "invalid_source_grant", "custom dependency reference must match its derived identity")
		}
	}
	digest, err := hex.DecodeString(grant.URLDigest)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != grant.URLDigest || grant.URLDigest != tx.urlDigest {
		return webAPIError(400, "invalid_source_grant", "invalid source URL digest")
	}
	if grant.Purpose == "" || grant.Scheme != "http" && grant.Scheme != "https" || grant.Host == "" || len(grant.Host) > 1024 || !utf8.ValidString(grant.Host) || strings.ContainsAny(grant.Host, "\x00\r\n") || grant.Port == 0 || grant.PolicyVersion <= 0 {
		return webAPIError(400, "invalid_source_grant", "invalid source authorization origin or policy")
	}
	if _, err = time.Parse(time.RFC3339Nano, grant.ExpiresAt); err != nil {
		return webAPIError(400, "invalid_source_grant", "invalid source authorization expiration")
	}
	if len(grant.Addresses) == 0 || len(grant.Addresses) > 128 {
		return webAPIError(400, "invalid_source_grant", "invalid source address set")
	}
	seen := map[netip.Addr]bool{}
	for _, value := range grant.Addresses {
		address, err := netip.ParseAddr(value)
		if err != nil || address.Zone() != "" || seen[address] {
			return webAPIError(400, "invalid_source_grant", "invalid or duplicate source address")
		}
		seen[address] = true
	}
	revision, err := webAPIRevision(tx.tx, "meta")
	if err != nil {
		return err
	}
	if grant.ExpectedRevision != revision || grant.ExpectedRevision != tx.expectedRevision {
		e := webAPIError(409, "revision_conflict", "source authorization preparation is stale")
		e.Revision = revision
		return e
	}
	bucket := tx.tx.Bucket([]byte("source-grants"))
	if bucket.Get([]byte(grant.ID)) != nil {
		return webAPIError(409, "duplicate_source_grant", "source authorization identity is already used")
	}
	return webAPIPut(tx.tx, "source-grants", grant.ID, grant)
}

func (tx *WebAPISourceAuthorizationTx) DeleteSourceGrant(id string) error {
	if !webAPISourceOpaqueKey(id) {
		return webAPIError(400, "invalid_source_grant", "invalid source authorization identity")
	}
	grant, err := tx.SourceGrant(id)
	if err != nil {
		var problem *WebAPIError
		if errors.As(err, &problem) && problem.Status == 404 {
			return nil
		}
		return err
	}
	if grant.SessionID != tx.sessionID {
		return webAPIError(403, "source_authorization_mismatch", "source authorization belongs to another session")
	}
	return tx.tx.Bucket([]byte("source-grants")).Delete([]byte(id))
}

func (s *WebAPIStore) SourceAuthorizationTransaction(ctx context.Context, operationID string, request WebAPISourceAuthorize, apply func(*WebAPISourceAuthorizationTx) (any, error)) (WebAPISourceAuthorizationReceipt, error) {
	var result WebAPISourceAuthorizationReceipt
	if !webAPIOperationID(operationID) || request.OperationID != operationID {
		return result, webAPIError(400, "invalid_operation_id", "source authorization operationId is invalid or inconsistent")
	}
	expected, err := webAPIInteger(string(request.ExpectedRevision), false)
	if err != nil || expected < 0 {
		return result, webAPIError(400, "invalid_revision", "expectedRevision must be a decimal revision string")
	}
	sessionID := WebAPIRequestSessionID(ctx)
	if sessionID == "" {
		return result, webAPIError(401, "unauthorized", "source authorization requires an authenticated session")
	}
	body, err := json.Marshal(struct {
		SessionID string                `json:"sessionId"`
		Request   WebAPISourceAuthorize `json:"request"`
	}{sessionID, request})
	if err != nil {
		return result, err
	}
	hash := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(hash[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	err = s.write(ctx, func(tx *bolt.Tx) error {
		receipts := tx.Bucket([]byte("source-receipts"))
		if raw := receipts.Get([]byte(operationID)); raw != nil {
			var saved webAPIStoredSourceReceipt
			if err := json.Unmarshal(raw, &saved); err != nil {
				return err
			}
			if saved.SessionID != sessionID {
				return webAPIError(403, "source_authorization_mismatch", "source authorization receipt belongs to another session")
			}
			if saved.Hash != fingerprint {
				return webAPIError(409, "operation_conflict", "operationId was used for a different source authorization request")
			}
			var err error
			result, err = webAPIReadSourceReceipt(tx, operationID, sessionID)
			return err
		}
		if err := webAPICheckSourceTombstone(tx, operationID, sessionID); err != nil {
			return err
		}
		revision, err := webAPIRevision(tx, "meta")
		if err != nil {
			return err
		}
		if request.ExpectedRevision != revision {
			e := webAPIError(409, "revision_conflict", "source authorization preparation is stale")
			e.Revision = revision
			return e
		}
		data, err := apply(&WebAPISourceAuthorizationTx{WebAPIReadTx: &WebAPIReadTx{tx}, sessionID: sessionID, expectedRevision: request.ExpectedRevision, urlDigest: request.URLDigest})
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		response, err := json.Marshal(WebAPIEnvelope[any]{Revision: revision, Data: data, OperationID: operationID})
		if err != nil {
			return err
		}
		if len(response) > webAPIMaxReceiptBytes {
			return webAPIError(413, "response_too_large", "source authorization response exceeds the receipt limit")
		}
		if err = webAPIPut(tx, "source-receipts", operationID, webAPIStoredSourceReceipt{Revision: revision, Hash: fingerprint, SessionID: sessionID, OperationID: operationID, Body: response}); err != nil {
			return err
		}
		result = WebAPISourceAuthorizationReceipt{Revision: revision, OperationID: operationID, Body: response}
		return nil
	})
	if err != nil {
		return WebAPISourceAuthorizationReceipt{}, err
	}
	return result, nil
}

func (tx *WebAPIWriteTx) InvalidateSourceGrantsForResource(resource WebAPISourceResource) error {
	if !webAPISourceOpaqueKey(resource.Kind) || !webAPISourceOpaqueKey(resource.ID) {
		return webAPIError(400, "invalid_source_resource", "invalid source resource identity")
	}
	bucket := tx.tx.Bucket([]byte("source-grants"))
	cursor := bucket.Cursor()
	for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
		var grant WebAPISourceGrant
		if err := json.Unmarshal(raw, &grant); err != nil {
			return err
		}
		if grant.Key != nil && grant.Key.Kind == resource.Kind && grant.Key.ID == resource.ID && (resource.Name == "" || grant.Key.Name == resource.Name) {
			if err := cursor.Delete(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (tx *WebAPIWriteTx) ResetAllSourceGrants() error {
	if err := tx.tx.DeleteBucket([]byte("source-grants")); err != nil {
		return err
	}
	_, err := tx.tx.CreateBucket([]byte("source-grants"))
	return err
}

func webAPISourceResourceBucket(kind string) string {
	return map[string]string{"profile": "profiles", "script": "scripts", "rule-provider": "rule-providers", "mitm-module": "mitm-modules", "mitm-resource": "mitm-modules", "icon-set": "icon-sets", "custom-import-dependency": "profiles"}[kind]
}
func webAPISourceBucketKind(bucket string) string {
	return map[string]string{"profiles": "profile", "scripts": "script", "rule-providers": "rule-provider", "mitm-modules": "mitm-module", "icon-sets": "icon-set"}[bucket]
}
func (tx *WebAPIWriteTx) invalidateSourceResource(bucket string, id WebAPIID) error {
	kind := webAPISourceBucketKind(bucket)
	if kind == "" {
		return nil
	}
	if err := tx.InvalidateSourceGrantsForResource(WebAPISourceResource{Kind: kind, ID: string(id)}); err != nil {
		return err
	}
	if kind == "profile" {
		return tx.InvalidateSourceGrantsForResource(WebAPISourceResource{Kind: "custom-import-dependency", ID: string(id)})
	}
	if kind == "mitm-module" {
		return tx.InvalidateSourceGrantsForResource(WebAPISourceResource{Kind: "mitm-resource", ID: string(id)})
	}
	return nil
}
func (tx *WebAPIWriteTx) invalidateChangedSource(bucket string, id WebAPIID, value any) error {
	changed := false
	switch next := value.(type) {
	case WebAPIProfile:
		prior, err := tx.Profile(id)
		if err != nil {
			return err
		}
		changed = prior.URL != next.URL || prior.Type != next.Type
	case WebAPIScript:
		prior, err := tx.Script(id)
		if err != nil {
			return err
		}
		changed = !reflect.DeepEqual(prior.URL, next.URL)
	case WebAPIClashProvider:
		prior, err := tx.ClashProvider(id)
		if err != nil {
			return err
		}
		changed = prior.URL != next.URL
	case WebAPIMitmModule:
		prior, err := tx.MitmModule(id)
		if err != nil {
			return err
		}
		changed = !reflect.DeepEqual(prior.URL, next.URL)
		if !reflect.DeepEqual(prior.Resources, next.Resources) {
			if err := tx.InvalidateSourceGrantsForResource(WebAPISourceResource{Kind: "mitm-resource", ID: string(id)}); err != nil {
				return err
			}
		}
	case WebAPIIconSet:
		prior, err := tx.IconSet(id)
		if err != nil {
			return err
		}
		changed = prior.URL != next.URL
	}
	if changed {
		return tx.invalidateSourceResource(bucket, id)
	}
	return nil
}

func webAPISourceDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func webAPISourceUint256Decimal(value string, allowZero bool) bool {
	if len(value) == 0 || len(value) > 78 {
		return false
	}
	integer, ok := new(big.Int).SetString(value, 10)
	return ok && integer.Sign() >= 0 && (allowZero || integer.Sign() > 0) && integer.BitLen() <= 256 && integer.String() == value
}
func webAPISourceGrantPurpose(key *WebAPISourceResource, purpose WebAPISourcePurpose) bool {
	if purpose == "custom-profile-import" {
		return key != nil && key.Kind == "profile" && key.ID != "" && key.Name == ""
	}
	if purpose == "custom-profile-dependency" {
		return key != nil && key.Kind == "custom-import-dependency" && key.ID != "" && webAPISourceOpaqueKey(key.Name)
	}
	if key != nil && key.Kind == "custom-import-dependency" {
		return false
	}

	if key != nil {
		switch key.Kind {
		case "dav":
			return key.ID == "settings" && key.Name == "" && webAPIContains([]string{"dav-probe", "dav-backup", "dav-restore"}, string(purpose))
		case "icon-image":
			return purpose == "icon-image" && key.Name == ""
		case "geo":
			return purpose == WebAPISourceGeoRefresh && key.Name == ""
		case "external-provider":
			return purpose == WebAPISourceExternalProviderRefresh && key.Name == ""
		}
	}
	return webSourceResourcePurpose(key, purpose)
}

func webAPICustomImportPurpose(purpose WebAPISourcePurpose) bool {
	return purpose == "custom-profile-import" || purpose == "custom-profile-dependency"
}
