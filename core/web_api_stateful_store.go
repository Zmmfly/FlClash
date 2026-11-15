//go:build webpanel && !android

package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const webStatefulBucket = "proxy-state-v1"

type webStatefulProfile struct {
	Incarnation string                           `json:"incarnation"`
	Resources   map[string]WebAPIStatefulBinding `json:"resources"`
}

func webStatefulNonce() (string, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

func webStatefulIdentity(store string, profile WebAPIID, incarnation, resource string) string {
	return webStatefulJSONDigest([]string{"proxy-state-v1", store, string(profile), incarnation, resource})
}

func (tx *WebAPIReadTx) statefulProfile(id WebAPIID) (webStatefulProfile, error) {
	profile := webStatefulProfile{Resources: map[string]WebAPIStatefulBinding{}}
	bucket := tx.tx.Bucket([]byte(webStatefulBucket))
	if bucket == nil {
		return profile, nil
	}
	if raw := bucket.Get([]byte("profile:" + string(id))); raw != nil {
		if err := json.Unmarshal(raw, &profile); err != nil || !webStatefulHex(profile.Incarnation) || profile.Resources == nil {
			return profile, webAPIError(409, "stateful_ledger_invalid", "private state binding metadata is invalid")
		}
	}
	return profile, nil
}

func (tx *WebAPIWriteTx) BindStatefulRuntime(operationID string, plan WebAPIStatefulPlan) error {
	if tx.authority == nil || tx.authority.sessionID == "" || tx.authority.operationID != operationID || !webAPIOperationID(operationID) {
		return webAPIError(409, "stateful_intent_required", "state binding requires the current authorized runtime transaction")
	}
	if err := webStatefulValidatePlan(plan); err != nil {
		return err
	}
	record, err := tx.runtimeRecord(operationID)
	if err != nil || record.State.Action != webRuntimeApply || record.State.Phase != WebAPIRuntimePending {
		return webAPIError(409, "stateful_intent_required", "state binding requires a recorded pending apply intent in this transaction")
	}
	var desired struct {
		Request struct {
			ProfileID *WebAPIID `json:"profileId"`
		} `json:"request"`
	}
	if json.Unmarshal(record.Desired, &desired) != nil || desired.Request.ProfileID == nil || *desired.Request.ProfileID != plan.ProfileID {
		return webAPIError(409, "stateful_intent_mismatch", "state binding does not match the runtime intent profile")
	}
	if _, err = tx.Profile(plan.ProfileID); err != nil {
		return err
	}
	for _, resource := range plan.Resources {
		if resource.Scope.Kind == WebAPIStatefulProfileProvider {
			if _, err = tx.Profile(resource.Scope.SourceProfileID); err != nil {
				return err
			}
		}
	}
	bucket, err := tx.tx.CreateBucketIfNotExists([]byte(webStatefulBucket))
	if err != nil {
		return err
	}
	if bucket.Get([]byte("operation:"+operationID)) != nil {
		return webAPIError(409, "stateful_intent_conflict", "runtime state bindings are immutable")
	}
	profile, err := tx.statefulProfile(plan.ProfileID)
	if err != nil {
		return err
	}
	if profile.Incarnation == "" {
		profile.Incarnation, err = webStatefulNonce()
		if err != nil {
			return err
		}
	}
	meta, err := tx.Meta()
	if err != nil {
		return err
	}
	bound := WebAPIStatefulBindings{StoreID: meta.StoreID, OperationID: operationID, ProfileID: plan.ProfileID, ProfileIncarnation: profile.Incarnation, Bindings: []WebAPIStatefulBinding{}}
	for _, resource := range plan.Resources {
		key := webStatefulResourceKey(resource)
		binding, exists := profile.Resources[key]
		if !exists || binding.Resource.SecurityDomain != resource.SecurityDomain {
			incarnation, err := webStatefulNonce()
			if err != nil {
				return err
			}
			binding = WebAPIStatefulBinding{Incarnation: incarnation, Identity: webStatefulIdentity(meta.StoreID, plan.ProfileID, profile.Incarnation, incarnation)}
		}
		binding.Resource = resource
		profile.Resources[key] = binding
		bound.Bindings = append(bound.Bindings, binding)
	}
	if err := webStatefulValidateBindings(bound); err != nil {
		return err
	}
	if err := webAPIPut(tx.tx, webStatefulBucket, "profile:"+string(plan.ProfileID), profile); err != nil {
		return err
	}
	return webAPIPut(tx.tx, webStatefulBucket, "operation:"+operationID, bound)
}

func webStatefulValidateBindings(bound WebAPIStatefulBindings) error {
	if bound.StoreID == "" || !webAPIOperationID(bound.OperationID) || !webStatefulHex(bound.ProfileIncarnation) {
		return webAPIError(409, "stateful_ledger_invalid", "private state binding metadata is invalid")
	}
	resources := make([]WebAPIStatefulResource, 0, len(bound.Bindings))
	seen := map[string]bool{}
	for _, binding := range bound.Bindings {
		if !webStatefulHex(binding.Incarnation) || binding.Identity != webStatefulIdentity(bound.StoreID, bound.ProfileID, bound.ProfileIncarnation, binding.Incarnation) || seen[binding.Identity] {
			return webAPIError(409, "stateful_ledger_invalid", "private state binding identity is invalid")
		}
		seen[binding.Identity] = true
		resources = append(resources, binding.Resource)
	}
	return webStatefulValidatePlan(WebAPIStatefulPlan{ProfileID: bound.ProfileID, Resources: resources})
}

func (store *WebAPIStore) StatefulRuntimeBindings(ctx context.Context, operationID string) (WebAPIStatefulBindings, error) {
	var bound WebAPIStatefulBindings
	if !webAPIOperationID(operationID) {
		return bound, webAPIError(400, "invalid_operation_id", "invalid operationId")
	}
	_, err := store.View(ctx, func(tx *WebAPIReadTx) error {
		if tx.tx.Bucket([]byte(webStatefulBucket)) == nil {
			return webAPIError(404, "stateful_binding_not_found", "runtime has no committed state binding")
		}
		if err := webAPIGet(tx.tx, webStatefulBucket, "operation:"+operationID, &bound); err != nil {
			return err
		}
		meta, err := tx.Meta()
		if err != nil {
			return err
		}
		if bound.StoreID != meta.StoreID || bound.OperationID != operationID {
			return webAPIError(409, "stateful_ledger_invalid", "state binding belongs to a different store or operation")
		}
		record, err := tx.runtimeRecord(operationID)
		if err != nil || record.State.Action != webRuntimeApply {
			return webAPIError(409, "stateful_intent_required", "committed state binding has no runtime intent")
		}
		return webStatefulValidateBindings(bound)
	})
	if err != nil {
		return WebAPIStatefulBindings{}, err
	}
	bound.committed = true
	return bound, nil
}

func (tx *WebAPIWriteTx) DetachStatefulProfiles(ids []WebAPIID) error {
	owners := map[WebAPIID]bool{}
	for _, id := range ids {
		if _, err := webAPIInteger(string(id), true); err != nil {
			return webAPIError(400, "invalid_id", "invalid stateful profile identity")
		}
		owners[id] = true
	}
	bucket := tx.tx.Bucket([]byte(webStatefulBucket))
	if bucket == nil || len(owners) == 0 {
		return nil
	}
	var remove []string
	updates := map[string]webStatefulProfile{}
	err := bucket.ForEach(func(key, raw []byte) error {
		name := string(key)
		if !strings.HasPrefix(name, "profile:") {
			return nil
		}
		if owners[WebAPIID(strings.TrimPrefix(name, "profile:"))] {
			remove = append(remove, name)
			return nil
		}
		var profile webStatefulProfile
		if err := json.Unmarshal(raw, &profile); err != nil {
			return err
		}
		changed := false
		for key, binding := range profile.Resources {
			if binding.Resource.Scope.Kind == WebAPIStatefulProfileProvider && !binding.Resource.Scope.Inline && owners[binding.Resource.Scope.SourceProfileID] {
				delete(profile.Resources, key)
				changed = true
			}
		}
		if changed {
			updates[name] = profile
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, key := range remove {
		if err := bucket.Delete([]byte(key)); err != nil {
			return err
		}
	}
	for key, profile := range updates {
		if err := webAPIPut(tx.tx, webStatefulBucket, key, profile); err != nil {
			return err
		}
	}
	return nil
}

func (tx *WebAPIWriteTx) DetachAllStatefulResources() error {
	bucket := tx.tx.Bucket([]byte(webStatefulBucket))
	if bucket == nil {
		return nil
	}
	var keys [][]byte
	cursor := bucket.Cursor()
	for key, _ := cursor.Seek([]byte("profile:")); key != nil && strings.HasPrefix(string(key), "profile:"); key, _ = cursor.Next() {
		keys = append(keys, append([]byte(nil), key...))
	}
	for _, key := range keys {
		if err := bucket.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

func (tx *WebAPIWriteTx) ReconcileStatefulResources(profileID WebAPIID, resources []WebAPIStatefulResource) error {
	plan := WebAPIStatefulPlan{ProfileID: profileID, Resources: resources}
	if err := webStatefulValidatePlan(plan); err != nil {
		return err
	}
	profile, err := tx.statefulProfile(profileID)
	if err != nil {
		return err
	}
	if profile.Incarnation == "" {
		return tx.reconcileStatefulConsumers(profileID, resources)
	}
	kept := map[string]WebAPIStatefulBinding{}
	for _, resource := range resources {
		key := webStatefulResourceKey(resource)
		if binding, exists := profile.Resources[key]; exists && binding.Resource.SecurityDomain == resource.SecurityDomain {
			binding.Resource = resource
			kept[key] = binding
		}
	}
	profile.Resources = kept
	if err := webAPIPut(tx.tx, webStatefulBucket, "profile:"+string(profileID), profile); err != nil {
		return err
	}
	return tx.reconcileStatefulConsumers(profileID, resources)
}

func webStatefulMemberKey(resource WebAPIStatefulResource) string {
	resource.Scope = WebAPIStatefulScope{Kind: WebAPIStatefulRoot}
	return webStatefulResourceKey(resource)
}

func (tx *WebAPIWriteTx) reconcileStatefulConsumers(sourceID WebAPIID, resources []WebAPIStatefulResource) error {
	bucket := tx.tx.Bucket([]byte(webStatefulBucket))
	if bucket == nil {
		return nil
	}
	members := map[string]WebAPIStatefulResource{}
	for _, resource := range resources {
		if resource.Scope.Kind == WebAPIStatefulRoot {
			members[webStatefulMemberKey(resource)] = resource
		}
	}
	updates := map[string]webStatefulProfile{}
	err := bucket.ForEach(func(key, raw []byte) error {
		if !strings.HasPrefix(string(key), "profile:") {
			return nil
		}
		var profile webStatefulProfile
		if err := json.Unmarshal(raw, &profile); err != nil {
			return err
		}
		changed := false
		for key, binding := range profile.Resources {
			resource := binding.Resource
			if resource.Scope.Kind != WebAPIStatefulProfileProvider || resource.Scope.Inline || resource.Scope.SourceProfileID != sourceID {
				continue
			}
			member, present := members[webStatefulMemberKey(resource)]
			if !present || member.SecurityDomain != resource.SecurityDomain {
				delete(profile.Resources, key)
				changed = true
			}
		}
		if changed {
			updates[string(key)] = profile
		}
		return nil
	})
	if err != nil {
		return err
	}
	for key, profile := range updates {
		if err := webAPIPut(tx.tx, webStatefulBucket, key, profile); err != nil {
			return err
		}
	}
	return nil
}
