//go:build webpanel && !android

package core

func (tx *WebAPIWriteTx) ReconcileStatefulScopeResources(profileID WebAPIID, scope WebAPIStatefulScope, resources []WebAPIStatefulResource) error {
	if err := webStatefulScope(scope); err != nil {
		return err
	}
	if scope.Inline && scope.SourceProfileID != profileID {
		return webAPIError(400, "invalid_stateful_scope", "inline providers must belong to their parent profile")
	}
	if err := webStatefulValidatePlan(WebAPIStatefulPlan{ProfileID: profileID, Resources: resources}); err != nil {
		return err
	}
	inventory := make(map[string]WebAPIStatefulResource, len(resources))
	for _, resource := range resources {
		if resource.Scope != scope {
			return webAPIError(400, "invalid_stateful_scope", "scoped inventory contains a resource from another scope")
		}
		inventory[webStatefulResourceKey(resource)] = resource
	}
	profile, err := tx.statefulProfile(profileID)
	if err != nil {
		return err
	}
	if profile.Incarnation != "" {
		for key, binding := range profile.Resources {
			if binding.Resource.Scope != scope {
				continue
			}
			resource, present := inventory[key]
			if !present || resource.SecurityDomain != binding.Resource.SecurityDomain {
				delete(profile.Resources, key)
			} else {
				binding.Resource = resource
				profile.Resources[key] = binding
			}
		}
		if err := webAPIPut(tx.tx, webStatefulBucket, "profile:"+string(profileID), profile); err != nil {
			return err
		}
	}
	if scope.Kind == WebAPIStatefulRoot {
		return tx.reconcileStatefulConsumers(profileID, resources)
	}
	return nil
}

func (tx *WebAPIWriteTx) DetachStatefulScopes(profileID WebAPIID, scopes []WebAPIStatefulScope) error {
	if _, err := webAPIInteger(string(profileID), true); err != nil {
		return webAPIError(400, "invalid_id", "invalid stateful profile identity")
	}
	selected := make(map[WebAPIStatefulScope]bool, len(scopes))
	for _, scope := range scopes {
		if err := webStatefulScope(scope); err != nil {
			return err
		}
		if scope.Kind == WebAPIStatefulRoot || scope.Inline && scope.SourceProfileID != profileID {
			return webAPIError(400, "invalid_stateful_scope", "provider detach requires an explicit matching provider scope")
		}
		selected[scope] = true
	}
	profile, err := tx.statefulProfile(profileID)
	if err != nil || profile.Incarnation == "" {
		return err
	}
	for key, binding := range profile.Resources {
		if selected[binding.Resource.Scope] {
			delete(profile.Resources, key)
		}
	}
	return webAPIPut(tx.tx, webStatefulBucket, "profile:"+string(profileID), profile)
}
