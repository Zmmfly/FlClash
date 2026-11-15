//go:build webpanel && !android

package core

type WebAPIStatefulScopeKind string

const (
	WebAPIStatefulRoot            WebAPIStatefulScopeKind = "root"
	WebAPIStatefulProfileProvider WebAPIStatefulScopeKind = "profile-provider"
	WebAPIStatefulRuntimeProvider WebAPIStatefulScopeKind = "runtime-provider"
	WebAPIStatefulMaxResources                            = 4096
)

type WebAPIStatefulScope struct {
	Kind            WebAPIStatefulScopeKind `json:"kind"`
	ProviderName    string                  `json:"providerName,omitempty"`
	SourceProfileID WebAPIID                `json:"sourceProfileId,omitempty"`
	OriginDomain    string                  `json:"originDomain,omitempty"`
	Inline          bool                    `json:"inline,omitempty"`
}

// Scope and ResourceIDs come only from the authoritative snapshot, never a request DTO.
type WebAPIStatefulInput struct {
	Scope        WebAPIStatefulScope
	Proxies      []map[string]any
	ResourceIDs  map[string]WebAPIID
	PlanetAssets []WebAPIStatefulPlanetAsset
}

type WebAPIStatefulResource struct {
	Scope          WebAPIStatefulScope `json:"scope"`
	Name           string              `json:"name"`
	ResourceID     WebAPIID            `json:"resourceId,omitempty"`
	Protocol       string              `json:"protocol"`
	SecurityDomain string              `json:"securityDomain"`
	PlanetSHA256   string              `json:"planetSha256,omitempty"`
}

type WebAPIStatefulPlan struct {
	ProfileID WebAPIID                 `json:"profileId"`
	Resources []WebAPIStatefulResource `json:"resources"`
}

type WebAPIStatefulBinding struct {
	Resource    WebAPIStatefulResource `json:"resource"`
	Identity    string                 `json:"identity"`
	Incarnation string                 `json:"incarnation"`
}

type WebAPIStatefulBindings struct {
	StoreID            string                  `json:"storeId"`
	OperationID        string                  `json:"operationId"`
	ProfileID          WebAPIID                `json:"profileId"`
	ProfileIncarnation string                  `json:"profileIncarnation"`
	Bindings           []WebAPIStatefulBinding `json:"bindings"`
	committed          bool
}

type WebAPIStatefulMaterialized struct {
	Resource WebAPIStatefulResource
	StateDir string
}
