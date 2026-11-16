//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/metacubex/mihomo/config"
	"go.yaml.in/yaml/v3"
)

type WebAPIRuntimeOptions struct {
	EffectTimeout      time.Duration
	WorkspaceRoot      string
	StatefulManager    *WebAPIStatefulManager
	SourceFetchOptions WebAPISourceFetchOptions
}

type WebAPIRuntimeResourceBindings struct {
	ProfileProviders map[string]WebAPIID `json:"profileProviders,omitempty"`
	RuleProviders    map[string]WebAPIID `json:"ruleProviders,omitempty"`
	MITMModules      []WebAPIID          `json:"mitmModules,omitempty"`
}

type WebAPIRuntimeBuildRequest struct {
	ID               WebAPIID                      `json:"id"`
	Overlay          *string                       `json:"overlay,omitempty"`
	CandidateYAML    *string                       `json:"candidateYaml,omitempty"`
	EvaluationID     string                        `json:"evaluationId,omitempty"`
	DialerProxies    map[string]string             `json:"dialerProxies,omitempty"`
	ResourceBindings WebAPIRuntimeResourceBindings `json:"resourceBindings,omitempty"`
}

type WebAPIRuntimeBuildResult struct {
	YAML   string `json:"yaml"`
	Digest string `json:"digest"`
}

type WebAPIRuntimePreparedApply struct {
	ConfigPath     string                   `json:"configPath"`
	Digest         string                   `json:"digest"`
	ProfileID      *WebAPIID                `json:"profileId"`
	SelectedMap    map[string]string        `json:"selectedMap"`
	Options        CoreOptions              `json:"options"`
	TopLevelCounts webRuntimeTopLevelCounts `json:"topLevelCounts"`
	stateful       *webRuntimeStatefulPreparation
	providers      []webRuntimeProviderWitness
	external       []webRuntimeExternalProvenance
	evaluation     *webRuntimeEvaluationProvenance
}

type WebAPIRuntimeApplyResult struct {
	ActualConfig    string     `json:"actualConfig"`
	Digest          string     `json:"digest,omitempty"`
	ProfileID       *WebAPIID  `json:"profileId"`
	Route           RouteState `json:"route"`
	ListenerEnabled bool       `json:"listenerEnabled"`
}

type webRuntimeDesiredApply struct {
	Request           WebAPIRuntimeApplyRequest       `json:"request"`
	Prepared          WebAPIRuntimePreparedApply      `json:"prepared"`
	Stateful          *webRuntimeStatefulPreparation  `json:"stateful,omitempty"`
	ProviderSources   []webRuntimeProviderWitness     `json:"providerSources,omitempty"`
	ExternalProviders []webRuntimeExternalProvenance  `json:"externalProviders,omitempty"`
	Evaluation        *webRuntimeEvaluationProvenance `json:"evaluation,omitempty"`
}

type webRuntimeDesiredReconcile struct {
	ProfileID *WebAPIID    `json:"profileId"`
	Patch     UpdateParams `json:"patch"`
}

type webRuntimeSource struct {
	Profile    WebAPIProfile
	Plain      []byte
	Evaluation *webRuntimeEvaluationProvenance
}
type webRuntimeRuleSource struct {
	Provider WebAPIClashProvider
	Bytes    []byte
}
type webRuntimeSnapshot struct {
	Revision     WebAPIRevision
	Settings     WebAPISettings
	Source       *webRuntimeSource
	Profiles     map[string]webRuntimeSource
	Rules        map[string]webRuntimeRuleSource
	Remote       map[string]webRuntimePreparedProvider
	ResourceIDs  map[string]WebAPIID
	PlanetAssets map[WebAPIID][]WebAPIStatefulPlanetAsset
}

type webRuntimeWorkspace struct {
	closed  bool
	mu      sync.Mutex
	options WebAPIRuntimeOptions
	root    *os.Root
	parent  *os.Root
	owned   string
}

func NewWebAPIRuntimeWithOptions(store *WebAPIStore, backend WebAPIRuntimeBackend, options WebAPIRuntimeOptions) (*WebAPIRuntime, error) {
	rt, err := NewWebAPIRuntime(store, backend)
	if err != nil {
		return nil, err
	}
	rt.workspace.options = options
	return rt, nil
}

func (w *webRuntimeWorkspace) initialize() error {
	if w.closed {
		return webAPIError(503, "runtime_closed", "runtime workspace is closed")
	}
	if w.root != nil {
		return nil
	}
	if w.options.WorkspaceRoot == "" {
		name, err := os.MkdirTemp("", "flclash-api-runtime-")
		if err != nil {
			return err
		}
		root, err := os.OpenRoot(name)
		if err != nil {
			os.RemoveAll(name)
			return err
		}
		w.root, w.owned = root, name
		return nil
	}
	base, err := webAPIPath(w.options.WorkspaceRoot)
	if err != nil {
		return err
	}
	parent, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		parent.Close()
		return err
	}
	name := ".api-runtime-" + hex.EncodeToString(random[:])
	if err = parent.Mkdir(name, 0o700); err != nil {
		parent.Close()
		return err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		parent.RemoveAll(name)
		parent.Close()
		return err
	}
	info, err := root.Stat(".")
	if err != nil || info.Mode().Perm() != 0o700 {
		root.Close()
		parent.RemoveAll(name)
		parent.Close()
		return errors.New("invalid runtime workspace")
	}
	w.root, w.parent, w.owned = root, parent, name
	return nil
}

func (w *webRuntimeWorkspace) put(data []byte, suffix string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(data) > WebAPIMaxContentBytes {
		return "", webAPIError(413, "content_too_large", "materialized content exceeds the resource limit")
	}
	if err := w.initialize(); err != nil {
		return "", webAPIError(503, "materialization_failed", "private runtime workspace is unavailable")
	}
	hash := sha256.Sum256(data)
	name := hex.EncodeToString(hash[:]) + suffix
	if info, err := w.root.Lstat(name); err == nil {
		if !webAPIPrivateFile(info) {
			return "", webAPIError(503, "materialization_failed", "private runtime blob is not a regular private file")
		}
		file, err := w.root.Open(name)
		if err != nil {
			return "", err
		}
		existing, err := io.ReadAll(io.LimitReader(file, WebAPIMaxContentBytes+1))
		file.Close()
		if err != nil || !bytes.Equal(existing, data) {
			return "", webAPIError(503, "materialization_failed", "private runtime blob identity changed")
		}
		return filepath.Join(w.root.Name(), name), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := w.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, writeErr := file.Write(data)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		w.root.Remove(name)
		return "", webAPIError(503, "materialization_failed", "private runtime blob could not be published")
	}
	return filepath.Join(w.root.Name(), name), nil
}

func (w *webRuntimeWorkspace) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.root == nil {
		return
	}
	w.root.Close()
	if w.parent != nil {
		w.parent.RemoveAll(w.owned)
		w.parent.Close()
	} else {
		os.RemoveAll(w.owned)
	}
	w.root = nil
}

func webRuntimeProfileSource(tx *WebAPIReadTx, id WebAPIID) (webRuntimeSource, error) {
	return webRuntimeProfileSourceBudget(tx, id, nil)
}

func webRuntimeProfileSourceBudget(tx *WebAPIReadTx, id WebAPIID, budget *webRuntimeSnapshotBudget) (webRuntimeSource, error) {
	var source webRuntimeSource
	if err := budget.record(tx, "profiles", string(id)); err != nil {
		return source, err
	}
	profile, err := tx.Profile(id)
	if err != nil {
		return source, err
	}
	if webAPIUnreadable(profile.WebAPIProfileInput) {
		return source, webAPIError(409, "profile_quarantined", "stored profile overrides are unreadable")
	}
	source.Profile = profile
	if profile.Type == "custom" {
		if err := tx.RequireProfileCatalogAvailable(id); err != nil {
			return source, err
		}
		if err := budget.metadata(tx); err != nil {
			return source, err
		}
		raw, _, err := webAPICatalogRawCandidate(tx, profile)
		if err != nil {
			return source, err
		}
		source.Plain, err = webRuntimeMarshalYAML(raw)
		return source, err
	}
	if _, err := tx.ProfileCatalog(id); err != nil {
		return source, webAPIError(409, "catalog_unavailable", "the committed profile has no validated source catalog")
	}
	key := WebAPIContentKey{Kind: WebAPIContentProfile, ID: id}
	content, err := budget.content(tx, key)
	if err != nil {
		return source, err
	}
	plain, err := budget.decompress(content.Bytes, key)
	if err != nil {
		return source, err
	}
	plain, err = webContentDecrypt(plain, profile.AgeSecretKey)
	if err != nil {
		return source, err
	}
	source.Plain = plain
	return source, nil
}

func (rt *WebAPIRuntime) snapshot(ctx context.Context, id *WebAPIID, bindings WebAPIRuntimeResourceBindings, candidate *WebAPIRuntimeApplyRequest) (webRuntimeSnapshot, error) {
	snapshot := webRuntimeSnapshot{Profiles: map[string]webRuntimeSource{}, Rules: map[string]webRuntimeRuleSource{}, Remote: map[string]webRuntimePreparedProvider{}}
	budget := &webRuntimeSnapshotBudget{}
	if candidate != nil {
		if candidate.Overlay != nil {
			if err := budget.reserve("request/overlay", int64(len(*candidate.Overlay))); err != nil {
				return snapshot, err
			}
		}
		if candidate.CandidateYAML != nil {
			if err := budget.reserve("request/candidate", int64(len(*candidate.CandidateYAML))); err != nil {
				return snapshot, err
			}
		}
	}
	profileSources := map[WebAPIID]webRuntimeSource{}
	rev, err := rt.store.View(ctx, func(tx *WebAPIReadTx) error {
		var err error
		if err = budget.bucket(tx, "settings"); err != nil {
			return err
		}
		snapshot.Settings, err = tx.Settings()
		if err != nil {
			return err
		}
		if id != nil {
			source, err := webRuntimeProfileSourceBudget(tx, *id, budget)
			if err != nil {
				return err
			}
			if candidate != nil {
				meta, err := tx.Meta()
				if err != nil {
					return err
				}
				if err = rt.resolveEvaluationSource(ctx, tx, &source, candidate.EvaluationID, meta.Revision, budget); err != nil {
					return err
				}
			}
			snapshot.Source = &source
			profileSources[*id] = source
			descriptors, err := webRuntimeProviderDescriptorsFromSource(source, rt.Events.storeID)
			if err != nil {
				return err
			}
			for _, descriptor := range descriptors {
				cached, err := budget.provider(tx, descriptor.Source.ID)
				if err != nil {
					var problem *WebAPIError
					if errors.As(err, &problem) && problem.Status == 404 {
						continue
					}
					return err
				}
				if cached.Phase == "ready" {
					cached.Descriptor = descriptor
					snapshot.Remote[descriptor.Source.Purpose+"/"+descriptor.Source.Name] = cached
				}
			}
		}
		for name, owner := range bindings.ProfileProviders {
			source, exists := profileSources[owner]
			if !exists {
				source, err = webRuntimeProfileSourceBudget(tx, owner, budget)
				if err != nil {
					return err
				}
				profileSources[owner] = source
			}
			if source.Profile.Type == "custom" || webAPIEffectiveProfileLabel(source.Profile) != name {
				return webAPIError(409, "invalid_resource_binding", "proxy-provider name must match the committed file or URL profile")
			}
			snapshot.Profiles[name] = source
		}
		for name, owner := range bindings.RuleProviders {
			if err := budget.record(tx, "rule-providers", string(owner)); err != nil {
				return err
			}
			provider, err := tx.ClashProvider(owner)
			if err != nil {
				return err
			}
			if provider.Label != name {
				return webAPIError(409, "invalid_resource_binding", "rule-provider name must match its committed owner")
			}
			content, err := budget.content(tx, WebAPIContentKey{Kind: WebAPIContentRuleProvider, ID: owner})
			if err != nil {
				return err
			}
			snapshot.Rules[name] = webRuntimeRuleSource{Provider: provider, Bytes: content.Bytes}
		}
		if len(bindings.MITMModules) > 0 {
			return webRuntimeUnavailable()
		}
		if snapshot.Source != nil && snapshot.Source.Profile.Type == "custom" {
			proxies, err := tx.CustomProxies()
			if err != nil {
				return err
			}
			snapshot.ResourceIDs = map[string]WebAPIID{}
			for _, proxy := range proxies {
				var name string
				if err := json.Unmarshal(proxy.Definition["name"], &name); err == nil && name != "" {
					snapshot.ResourceIDs[name] = proxy.ID
				}
			}
		}
		if candidate != nil {
			return webRuntimeSnapshotPlanetAssets(tx, &snapshot, *candidate, budget)
		}
		return nil
	})
	snapshot.Revision = rev
	return snapshot, err
}

func webRuntimeDocument(data []byte) (*yaml.Node, error) {
	if len(data) > WebAPIMaxContentBytes {
		return nil, webAPIError(413, "content_too_large", "candidate YAML exceeds the resource limit")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, webAPIError(400, "invalid_candidate", "candidate must be UTF-8 YAML")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc, second yaml.Node
	if decoder.Decode(&doc) != nil || decoder.Decode(&second) != io.EOF {
		return nil, webAPIError(400, "invalid_candidate", "one YAML document is required")
	}
	if err := WebAPIValidateYAMLWork(&doc); err != nil {
		return nil, err
	}
	if err := validateDependencyDocumentTree(&doc); err != nil {
		return nil, err
	}
	root, err := documentMapping(&doc)
	if err != nil {
		return nil, webAPIError(400, "invalid_candidate", "candidate must be a mapping")
	}
	count := 0
	stack := []struct {
		node  *yaml.Node
		depth int
	}{{root, 0}}
	seen := map[*yaml.Node]bool{}
	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[item.node] {
			continue
		}
		seen[item.node] = true
		count++
		if count > 1000000 || item.depth > 128 {
			return nil, webAPIError(413, "candidate_too_complex", "candidate YAML exceeds the structural budget")
		}
		for _, child := range item.node.Content {
			stack = append(stack, struct {
				node  *yaml.Node
				depth int
			}{child, item.depth + 1})
		}
		if item.node.Alias != nil {
			stack = append(stack, struct {
				node  *yaml.Node
				depth int
			}{item.node.Alias, item.depth + 1})
		}
	}
	retagShortIDs(root)
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, nil
}

func webRuntimeCandidate(snapshot webRuntimeSnapshot, p WebAPIRuntimeApplyRequest) (*yaml.Node, error) {
	if p.ProfileID == nil {
		if p.Overlay != nil || p.CandidateYAML != nil || len(p.DialerProxies) > 0 || len(p.ResourceBindings.ProfileProviders) > 0 || len(p.ResourceBindings.RuleProviders) > 0 {
			return nil, webAPIError(400, "invalid_setup_source", "without a profile only the default empty source is allowed")
		}
		return webRuntimeDocument([]byte("{}"))
	}
	if snapshot.Source == nil {
		return nil, webAPIError(409, "source_unavailable", "committed source is unavailable")
	}
	if p.CandidateYAML != nil {
		if p.Overlay != nil || snapshot.Source.Profile.Type != "custom" && snapshot.Source.Profile.ExtendType != "script" {
			return nil, webAPIError(400, "invalid_setup_source", "only a custom or script profile accepts a final candidate")
		}
		return webRuntimeDocument([]byte(*p.CandidateYAML))
	}
	doc, err := webRuntimeDocument(snapshot.Source.Plain)
	if err != nil {
		return nil, err
	}
	if p.Overlay == nil {
		return doc, nil
	}
	overlay, err := webRuntimeDocument([]byte(*p.Overlay))
	if err != nil {
		return nil, err
	}
	profile := doc.Content[0]
	root := overlay.Content[0]
	for _, section := range profileBaseSections {
		if node := mappingValue(profile, section); node != nil {
			setMappingValue(root, section, node)
		}
	}
	rules, err := appendRules(mappingValue(root, "rules"), mappingValue(profile, "rules"))
	if err != nil {
		return nil, err
	}
	if rules != nil {
		setMappingValue(root, "rules", rules)
	}
	return overlay, nil
}

func webRuntimeCanonicalOptions(settings WebAPISettings) CoreOptions {
	return CoreOptions{TestURL: settings.Runtime.PatchClashConfig.TestURL, SkipCertVerify: !settings.Runtime.AppSetting.CheckCertificate, DelayTestTimeout: int64(settings.Runtime.AppSetting.DelayTestTimeout), MITMCapture: settings.Runtime.PatchClashConfig.Mitm.Capture}
}

func webRuntimeValidateApplyAuthority(snapshot webRuntimeSnapshot, p WebAPIRuntimeApplyRequest) error {
	if snapshot.Revision != p.ExpectedRevision {
		return webAPIError(409, "revision_conflict", "candidate sources do not match expectedRevision")
	}
	if !webAPIOwner(snapshot.Settings.CurrentProfileID, p.ProfileID) {
		return webAPIError(409, "profile_not_committed", "the selected profile must be committed before runtime apply")
	}
	picks := map[string]string{}
	if snapshot.Source != nil {
		picks = snapshot.Source.Profile.SelectedMap
	}
	if p.SelectedMap == nil || !maps.Equal(picks, p.SelectedMap) {
		return webAPIError(409, "selection_not_committed", "runtime selections must match the committed profile")
	}
	if !reflect.DeepEqual(webRuntimeCanonicalOptions(snapshot.Settings), p.Options) {
		return webAPIError(409, "options_not_committed", "runtime options must match the committed settings")
	}
	return nil
}

func webRuntimeInlinePEM(text, key string) bool {
	rest := []byte(text)
	found := false
	for len(bytes.TrimSpace(rest)) > 0 {
		block, next := pem.Decode(rest)
		if block == nil {
			return false
		}
		if key == "private-key" {
			if !strings.HasSuffix(block.Type, "PRIVATE KEY") {
				return false
			}
		} else if block.Type != "CERTIFICATE" {
			return false
		}
		found = true
		rest = next
	}
	return found
}

func webRuntimePathPolicy(value any, context string) error {
	switch value := value.(type) {
	case map[string]any:
		if _, err := WebAPIValidateStatefulDefinition(value); err != nil {
			return err
		}
		for key, item := range value {
			switch strings.ToLower(key) {
			case "external-controller", "external-controller-tls", "external-controller-unix", "external-controller-pipe", "external-ui", "external-ui-url", "state-dir", "config-file", "planet", "ca-file", "cert-file", "key-file", "client-auth-cert", "ech-key":
				if !webRuntimeEmptyField(item) {
					return webAPIError(422, "resource_required", "external controllers and unbound filesystem resources are not accepted")
				}
			case "path":
				if (strings.Contains(context, "/proxy-providers/") || strings.Contains(context, "/rule-providers/")) && !webRuntimeEmptyField(item) {
					return webAPIError(422, "resource_required", "provider paths require a typed resource binding")
				}
			case "certificate", "private-key", "ca":
				text, ok := item.(string)
				kind, _ := value["type"].(string)
				if !ok {
					return webAPIError(400, "invalid_certificate", "certificate and key fields must be strings")
				}
				if text != "" {
					inline := webRuntimeInlinePEM(text, key)
					if key == "private-key" && (kind == "wireguard" || kind == "masque") {
						decoded, err := base64.StdEncoding.DecodeString(text)
						inline = err == nil && len(decoded) == 32
					}
					if !inline {
						return webAPIError(422, "resource_required", "certificate and key paths are not accepted")
					}
				}
			}
			if err := webRuntimePathPolicy(item, context+"/"+key); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := webRuntimePathPolicy(item, context); err != nil {
				return err
			}
		}
	}
	return nil
}

// Bound profiles never refresh, so inlining lets dialer checks see through them.
func webRuntimeInlineProfileProviders(root map[string]any, snapshot webRuntimeSnapshot) error {
	declared, _ := root["proxy-providers"].(map[string]any)
	for name, raw := range declared {
		mapping, _ := raw.(map[string]any)
		source, bound := snapshot.Profiles[name]
		if mapping == nil || !bound || mapping["type"] == "inline" {
			continue
		}
		doc, err := webRuntimeDocument(source.Plain)
		if err != nil {
			return err
		}
		var payload map[string]any
		if err = doc.Decode(&payload); err != nil {
			return err
		}
		proxies, _ := payload["proxies"].([]any)
		if proxies == nil {
			proxies = []any{}
		}
		mapping["type"], mapping["payload"] = "inline", proxies
		delete(mapping, "path")
		delete(mapping, "interval")
	}
	return nil
}

func (rt *WebAPIRuntime) bindResources(root map[string]any, snapshot webRuntimeSnapshot, materialize bool) error {
	for _, category := range []string{"proxy-providers", "rule-providers"} {
		declared, ok := root[category].(map[string]any)
		if !ok {
			if root[category] != nil {
				return webAPIError(400, "invalid_provider", "provider declarations must be maps")
			}
			declared = map[string]any{}
		}
		for name, raw := range declared {
			mapping, ok := raw.(map[string]any)
			if !ok {
				return webAPIError(400, "invalid_provider", "provider declaration must be a map")
			}
			if mapping["type"] == "inline" {
				if !webRuntimeEmptyField(mapping["url"]) {
					return webAPIError(400, "invalid_inline_provider", "inline providers cannot name a remote URL")
				}
				payload, ok := mapping["payload"].([]any)
				if !ok {
					return webAPIError(400, "invalid_inline_provider", "inline provider payload must be a list")
				}
				if category == "proxy-providers" {
					definitions := make([]map[string]any, 0, len(payload))
					for _, item := range payload {
						definition, ok := item.(map[string]any)
						if !ok {
							return webAPIError(400, "invalid_inline_provider", "inline proxy payload must contain definitions")
						}
						definitions = append(definitions, definition)
					}
					if err := webRuntimeDefinitions(definitions); err != nil {
						return err
					}
				} else {
					for _, item := range payload {
						if _, ok := item.(string); !ok {
							return webAPIError(400, "invalid_inline_provider", "inline rule payload must contain strings")
						}
					}
				}
				continue
			}
			_, profileBound := snapshot.Profiles[name]
			_, ruleBound := snapshot.Rules[name]
			if category == "proxy-providers" && !profileBound || category == "rule-providers" && !ruleBound {
				cached, ready := snapshot.Remote[category+"/"+name]
				if !ready {
					return webAPIError(409, "provider_source_unavailable", "prepare the purpose-bound provider source before applying")
				}
				if mapping["url"] != cached.Descriptor.URL {
					return webAPIError(409, "source_target_changed", "candidate provider URL is not the committed source")
				}
				path := "flclash-source:" + string(cached.Descriptor.Source.ID)
				if materialize {
					var err error
					path, err = rt.workspace.put(cached.Bytes, ".provider")
					if err != nil {
						return err
					}
				}
				mapping["type"], mapping["path"], mapping["interval"] = "file", path, 0
				delete(mapping, "url")
				delete(mapping, "header")
				if category == "rule-providers" {
					mapping["format"], mapping["behavior"] = cached.Descriptor.Format, cached.Descriptor.Behavior
				}
				continue
			}
			if category == "proxy-providers" {
				source, bound := snapshot.Profiles[name]
				if !bound {
					return webAPIError(409, "provider_source_unavailable", "a purpose-bound proxy-provider source must be prepared")
				}
				doc, err := webRuntimeDocument(source.Plain)
				if err != nil {
					return err
				}
				var payload map[string]any
				if err = doc.Decode(&payload); err != nil {
					return err
				}
				if err = webRuntimePathPolicy(payload["proxies"], ""); err != nil {
					return err
				}
				data, err := webRuntimeMarshalYAML(map[string]any{"proxies": payload["proxies"]})
				if err != nil {
					return err
				}
				path := "flclash-resource:profile:" + string(source.Profile.ID)
				if materialize {
					path, err = rt.workspace.put(data, ".yaml")
					if err != nil {
						return err
					}
				}
				mapping["type"], mapping["path"], mapping["interval"] = "file", path, 0
				delete(mapping, "url")
				delete(mapping, "header")
			} else {
				source, bound := snapshot.Rules[name]
				if !bound {
					return webAPIError(409, "provider_source_unavailable", "a purpose-bound rule-provider source must be prepared")
				}
				path := "flclash-resource:rule-provider:" + string(source.Provider.ID)
				var err error
				if materialize {
					path, err = rt.workspace.put(source.Bytes, ".rules")
					if err != nil {
						return err
					}
				}
				mapping["type"], mapping["path"], mapping["behavior"], mapping["format"], mapping["interval"] = "file", path, source.Provider.Behavior, source.Provider.Format, 0
				delete(mapping, "url")
				delete(mapping, "header")
			}
		}
		for name := range snapshot.Profiles {
			if category == "proxy-providers" && declared[name] == nil {
				return webAPIError(409, "invalid_resource_binding", "an unused proxy-provider binding was supplied")
			}
		}
		for name := range snapshot.Rules {
			if category == "rule-providers" && declared[name] == nil {
				return webAPIError(409, "invalid_resource_binding", "an unused rule-provider binding was supplied")
			}
		}
		if len(declared) > 0 {
			root[category] = declared
		}
	}
	return nil
}

func (rt *WebAPIRuntime) prepareApply(ctx context.Context, p WebAPIRuntimeApplyRequest, materialize bool) (WebAPIRuntimePreparedApply, WebAPIRuntimeBuildResult, error) {
	var prepared WebAPIRuntimePreparedApply
	var result WebAPIRuntimeBuildResult
	snapshot, err := rt.snapshot(ctx, p.ProfileID, p.ResourceBindings, &p)
	if err != nil {
		return prepared, result, err
	}
	if materialize {
		if err = webRuntimeValidateApplyAuthority(snapshot, p); err != nil {
			return prepared, result, err
		}
	} else if snapshot.Revision != p.ExpectedRevision {
		return prepared, result, webAPIError(409, "revision_conflict", "profile sources changed during preview")
	}
	if p.Options.MITMCapture && rt.backend.MITM == nil {
		return prepared, result, webRuntimeUnavailable()
	}
	document, err := webRuntimeCandidate(snapshot, p)
	if err != nil {
		return prepared, result, err
	}
	if len(p.DialerProxies) > 0 {
		if err = applyDocumentDialers(document, p.DialerProxies); err != nil {
			return prepared, result, err
		}
	}
	var root map[string]any
	if err = document.Decode(&root); err != nil {
		return prepared, result, webAPIError(400, "invalid_candidate", "candidate YAML cannot be decoded")
	}
	if err = webRuntimePathPolicy(root, ""); err != nil {
		return prepared, result, err
	}
	root["geo-auto-update"] = false
	publicDocument, err := webRuntimeMarshalYAML(root)
	if err != nil {
		return prepared, result, err
	}
	document, err = webRuntimeDocument(publicDocument)
	if err != nil {
		return prepared, result, err
	}
	public, err := webRuntimeMarshalYAML(document)
	if err != nil {
		return prepared, result, err
	}
	if err = rt.bindResources(root, snapshot, materialize); err != nil {
		return prepared, result, err
	}
	stateful, err := webRuntimePrepareStateful(root, snapshot, p.ProfileID)
	if err != nil {
		return prepared, result, err
	}
	providers := webRuntimeProviderWitnesses(root, snapshot)
	external := webRuntimeExternalProvenanceFor(root, snapshot)
	if err = webRuntimeInlineProfileProviders(root, snapshot); err != nil {
		return prepared, result, err
	}
	bound, err := webRuntimeMarshalYAML(root)
	if err != nil {
		return prepared, result, err
	}
	document, err = webRuntimeDocument(bound)
	if err != nil {
		return prepared, result, err
	}
	actualRoot, err := prepareDependencyDocument(document, nil)
	if err != nil {
		return prepared, result, err
	}
	counts := webRuntimeTopLevelCounts{Groups: len(actualRoot.ProxyGroup), Proxies: len(actualRoot.Proxy), Rules: len(actualRoot.Rule)}
	bound, err = webRuntimeMarshalYAML(document)
	if err != nil {
		return prepared, result, err
	}
	inputs := map[string]string{}
	for name, source := range snapshot.Profiles {
		hash := sha256.Sum256(source.Plain)
		inputs["profile:"+name] = hex.EncodeToString(hash[:])
	}
	for name, source := range snapshot.Rules {
		hash := sha256.Sum256(source.Bytes)
		inputs["rule:"+name] = hex.EncodeToString(hash[:])
	}
	identity, _ := json.Marshal(struct {
		YAML    string
		Sources map[string]string
		Options CoreOptions
		Picks   map[string]string
	}{string(public), inputs, p.Options, p.SelectedMap})
	digest := sha256.Sum256(identity)
	prepared = WebAPIRuntimePreparedApply{Digest: hex.EncodeToString(digest[:]), ProfileID: p.ProfileID, SelectedMap: maps.Clone(p.SelectedMap), Options: p.Options, TopLevelCounts: counts, stateful: stateful, providers: providers, external: external}
	if snapshot.Source != nil {
		prepared.evaluation = snapshot.Source.Evaluation
	}
	if materialize && rt.backend.Native {
		if err = rt.ensureGeoReady(ctx); err != nil {
			return prepared, result, err
		}
	}
	if materialize {
		prepared.ConfigPath, err = rt.workspace.put(bound, ".yaml")
		if err != nil {
			return prepared, result, err
		}
	}
	result = WebAPIRuntimeBuildResult{YAML: string(public), Digest: prepared.Digest}
	return prepared, result, nil
}

var webRuntimeNeedsPreparation = errors.New("runtime preparation required")

func (rt *WebAPIRuntime) preflightApply(ctx context.Context, m WebAPIMutation, command string, p any) (WebAPIReceipt, bool, error) {
	receipt, err := rt.store.Transaction(ctx, m, command, p, func(*WebAPIWriteTx) (any, error) { return nil, webRuntimeNeedsPreparation })
	if errors.Is(err, webRuntimeNeedsPreparation) {
		return WebAPIReceipt{}, false, nil
	}
	return receipt, err == nil, err
}

func (rt *WebAPIRuntime) applyHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	var p WebAPIRuntimeApplyRequest
	if err := WebAPIDecodeJSONLimit(w, r, &p, WebAPIRuntimeBodyLimit(r.URL.Path)); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	const command = "runtime:apply"
	if receipt, found, err := rt.preflightApply(r.Context(), p.WebAPIMutation, command, p); err != nil {
		WebAPIWriteError(w, err)
		return
	} else if found {
		WebAPIWriteReceipt(w, receipt)
		return
	}
	if rt.backend.SafeMode {
		WebAPIWriteError(w, webRuntimeSafeMode())
		return
	}
	if rt.backend.Apply == nil {
		WebAPIWriteError(w, webRuntimeUnavailable())
		return
	}
	prepared, _, err := rt.prepareApply(r.Context(), p, true)
	if err != nil {
		WebAPIWriteError(w, webRuntimeFailure(err))
		return
	}
	receipt, err := rt.store.Transaction(r.Context(), p.WebAPIMutation, command, p, func(tx *WebAPIWriteTx) (any, error) {
		if err := webRuntimeValidateProviderWitnesses(tx.WebAPIReadTx, prepared.providers); err != nil {
			return nil, err
		}
		state, err := tx.RecordRuntimeIntent(p.OperationID, webRuntimeApply, webRuntimeDesiredApply{Request: p, Prepared: prepared, Stateful: prepared.stateful, ProviderSources: prepared.providers, ExternalProviders: prepared.external, Evaluation: prepared.evaluation})
		if err != nil {
			return nil, err
		}
		if prepared.stateful != nil {
			if err = tx.BindStatefulRuntime(p.OperationID, prepared.stateful.Plan); err != nil {
				return nil, err
			}
		}
		return state, nil
	})
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if !receipt.Replayed {
		select {
		case rt.wake <- struct{}{}:
		default:
		}
	}
	WebAPIWriteReceipt(w, receipt)
}

func webRuntimeApplyNative(_ context.Context, p WebAPIRuntimePreparedApply) (WebAPIRuntimeApplyResult, error) {
	result := WebAPIRuntimeApplyResult{ActualConfig: "candidate", Digest: p.Digest, ProfileID: p.ProfileID}
	err := handleSetupConfig(&SetupParams{ConfigPath: p.ConfigPath, SelectedMap: p.SelectedMap, Options: p.Options})
	result.Route, result.ListenerEnabled = routeState(), isRunning.Load()
	if err != nil {
		var method *MethodError
		if errors.As(err, &method) && method.Code == "config_rejected" {
			result.ActualConfig, result.Digest, result.ProfileID = "default-fallback", "", nil
			return result, webAPIError(422, "config_rejected", "the core rejected the candidate and installed its default fallback")
		}
		result.ActualConfig = "unknown"
		return result, err
	}
	return result, nil
}

func (rt *WebAPIRuntime) reconcileHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	var p WebAPIMutation
	if err := WebAPIDecodeJSON(w, r, &p); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if receipt, found, err := rt.preflightApply(r.Context(), p, "runtime:reconcile", p); err != nil {
		WebAPIWriteError(w, err)
		return
	} else if found {
		WebAPIWriteReceipt(w, receipt)
		return
	}
	if rt.backend.SafeMode {
		WebAPIWriteError(w, webRuntimeSafeMode())
		return
	}
	if rt.backend.Reconcile == nil {
		WebAPIWriteError(w, webRuntimeUnavailable())
		return
	}
	receipt, err := rt.store.Transaction(r.Context(), p, "runtime:reconcile", p, func(tx *WebAPIWriteTx) (any, error) {
		settings, err := tx.Settings()
		if err != nil {
			return nil, err
		}
		patch, err := webRuntimeSettingsPatch(settings)
		if err != nil {
			return nil, err
		}
		return tx.RecordRuntimeIntent(p.OperationID, webRuntimeReconcile, webRuntimeDesiredReconcile{ProfileID: settings.CurrentProfileID, Patch: patch})
	})
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if !receipt.Replayed {
		select {
		case rt.wake <- struct{}{}:
		default:
		}
	}
	WebAPIWriteReceipt(w, receipt)
}

func webRuntimeSettingsPatch(settings WebAPISettings) (UpdateParams, error) {
	var patch UpdateParams
	if err := ValidateWebAPISettings(settings); err != nil {
		return patch, err
	}
	c := settings.Runtime.PatchClashConfig
	data := map[string]any{"tun": map[string]any{"enable": c.Tun.Enable, "device": c.Tun.Device, "stack": c.Tun.Stack, "auto-route": true}, "allow-lan": c.AllowLAN, "mixed-port": c.MixedPort, "find-process-mode": c.FindProcessMode, "mode": c.Mode, "log-level": c.LogLevel, "external-controller": "", "secret": "", "geo-auto-update": false, "geo-update-interval": c.GeoUpdateInterval, "options": webRuntimeCanonicalOptions(settings)}
	for key, value := range map[string]any{"ipv6": c.IPv6, "tcp-concurrent": c.TCPConcurrent, "unified-delay": c.UnifiedDelay} {
		if webAPIContains(c.GeneralOverrideKeys, key) {
			data[key] = value
		}
	}
	auth := settings.Network.NetworkProps.Authentication
	credentials := []string{}
	if auth.Enable {
		credentials = append(credentials, auth.Username+":"+auth.Password)
	}
	data["authentication"] = credentials
	encoded, err := json.Marshal(data)
	if err != nil {
		return patch, err
	}
	err = json.Unmarshal(encoded, &patch)
	return patch, err
}

func (rt *WebAPIRuntime) buildHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	var request WebAPIRuntimeBuildRequest
	if err := WebAPIDecodeJSONLimit(w, r, &request, 48<<20); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	planetCandidate := WebAPIRuntimeApplyRequest{ProfileID: &request.ID, Overlay: request.Overlay, CandidateYAML: request.CandidateYAML, EvaluationID: request.EvaluationID, ResourceBindings: request.ResourceBindings}
	snapshot, err := rt.snapshot(r.Context(), &request.ID, request.ResourceBindings, &planetCandidate)
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	p := WebAPIRuntimeApplyRequest{WebAPIMutation: WebAPIMutation{ExpectedRevision: snapshot.Revision}, ProfileID: &request.ID, Overlay: request.Overlay, CandidateYAML: request.CandidateYAML, EvaluationID: request.EvaluationID, DialerProxies: request.DialerProxies, ResourceBindings: request.ResourceBindings, Options: webRuntimeCanonicalOptions(snapshot.Settings)}
	if snapshot.Source != nil {
		p.SelectedMap = snapshot.Source.Profile.SelectedMap
	}
	_, result, err := rt.prepareApply(r.Context(), p, false)
	if err != nil {
		WebAPIWriteError(w, webRuntimeFailure(err))
		return
	}
	rt.profileAnswer(w, r, snapshot.Revision, result)
}

type WebAPIRuntimeSetupState struct {
	Profile          WebAPIProfile         `json:"profile"`
	Settings         WebAPISettings        `json:"settings"`
	Groups           []WebAPIProxyGroup    `json:"groups"`
	Rules            []WebAPIRule          `json:"rules"`
	GlobalRules      []WebAPIRule          `json:"globalRules"`
	DisabledRuleIDs  []WebAPIID            `json:"disabledRuleIds"`
	CustomProxies    []WebAPICustomProxy   `json:"customProxies"`
	RuleProviders    []WebAPIClashProvider `json:"ruleProviders"`
	Script           *WebAPIScript         `json:"script"`
	ScriptContent    *WebAPIContentResult  `json:"scriptContent"`
	MITMModules      []WebAPIMitmModule    `json:"mitmModules"`
	SelectedMap      map[string]string     `json:"selectedMap"`
	Options          CoreOptions           `json:"options"`
	Base             *ProfileBase          `json:"base"`
	ProxyDialers     []WebAPIProxyDialer   `json:"proxyDialers"`
	ProfileProviders map[string]WebAPIID   `json:"profileProviders"`
}

func webRuntimeBase(data []byte) (*ProfileBase, *config.RawConfig, error) {
	document, err := webRuntimeDocument(data)
	if err != nil {
		return nil, nil, err
	}
	raw, err := prepareDependencyDocument(document, nil)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(data)
	base := &ProfileBase{Digest: hex.EncodeToString(digest[:]), MatchRules: []string{}, Proxies: len(raw.Proxy), ProxyGroups: len(raw.ProxyGroup), Rules: len(raw.Rule)}
	for _, rule := range raw.Rule {
		if isMatchRule(rule) {
			base.MatchRules = append(base.MatchRules, rule)
		}
	}
	slim := *raw
	slim.Proxy, slim.ProxyGroup, slim.Rule, slim.SubRules = nil, nil, nil, nil
	base.Config = &slim
	return base, raw, nil
}

func WebAPIRuntimeSetupSnapshot(tx *WebAPIReadTx, id WebAPIID) (WebAPIRuntimeSetupState, error) {
	var result WebAPIRuntimeSetupState
	source, err := webRuntimeProfileSource(tx, id)
	if err != nil {
		return result, err
	}
	result.Profile = source.Profile
	result.Base, _, err = webRuntimeBase(source.Plain)
	if err != nil {
		return result, err
	}
	settings, err := tx.Settings()
	if err != nil {
		return result, err
	}
	result.Settings = PublicWebAPISettings(settings)
	result.Options = webRuntimeCanonicalOptions(settings)
	result.SelectedMap = maps.Clone(source.Profile.SelectedMap)
	groups, err := tx.ProxyGroups()
	if err != nil {
		return result, err
	}
	result.Groups = []WebAPIProxyGroup{}
	for _, group := range groups {
		if group.ProfileID == id {
			result.Groups = append(result.Groups, group)
		}
	}
	dialers, err := tx.ProxyDialers()
	if err != nil {
		return result, err
	}
	result.ProxyDialers = []WebAPIProxyDialer{}
	for _, dialer := range dialers {
		if dialer.ProfileID == id {
			result.ProxyDialers = append(result.ProxyDialers, dialer)
		}
	}
	profiles, err := tx.Profiles()
	if err != nil {
		return result, err
	}
	result.ProfileProviders = map[string]WebAPIID{}
	for _, profile := range profiles {
		if profile.Type == "custom" {
			continue
		}
		name := webAPIEffectiveProfileLabel(profile)
		if _, duplicate := result.ProfileProviders[name]; duplicate {
			return result, webAPIError(409, "catalog_unavailable", "profile provider names are ambiguous")
		}
		result.ProfileProviders[name] = profile.ID
	}
	result.Rules, err = tx.OwnedRules(&id)
	if err != nil {
		return result, err
	}
	result.GlobalRules, err = tx.OwnedRules(nil)
	if err != nil {
		return result, err
	}
	disabled, err := tx.DisabledSelection(id)
	if err != nil {
		return result, err
	}
	result.DisabledRuleIDs = disabled.RuleIDs
	result.CustomProxies, err = tx.CustomProxies()
	if err != nil {
		return result, err
	}
	result.RuleProviders, err = tx.ClashProviders()
	if err != nil {
		return result, err
	}
	result.MITMModules, err = tx.MitmModules()
	if err != nil {
		return result, err
	}
	if source.Profile.ScriptID != nil {
		script, err := tx.Script(*source.Profile.ScriptID)
		if err != nil {
			return result, err
		}
		result.Script = &script
		content, err := tx.Content(WebAPIContentKey{Kind: WebAPIContentScript, ID: script.ID})
		if err != nil {
			return result, err
		}
		value := webContentResult(content)
		result.ScriptContent = &value
	}
	return result, nil
}

func (rt *WebAPIRuntime) profileAnswer(w http.ResponseWriter, r *http.Request, revision WebAPIRevision, data any) {
	release, err := WebAPIAcquireCommitAuthorization(r.Context())
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	release()
	WebAPIWriteSuccess(w, revision, data)
}

func (rt *WebAPIRuntime) profileDerivedHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/profiles/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		WebAPIWriteError(w, webAPIError(404, "not_found", "profile derived resource does not exist"))
		return
	}
	if _, err := webAPIInteger(parts[0], true); err != nil {
		WebAPIWriteError(w, webAPIError(400, "invalid_id", "a decimal profile identity is required"))
		return
	}
	id := WebAPIID(parts[0])
	var result any
	revision, err := rt.store.View(r.Context(), func(tx *WebAPIReadTx) error {
		if parts[1] == "setup-state" {
			var err error
			result, err = WebAPIRuntimeSetupSnapshot(tx, id)
			return err
		}
		source, err := webRuntimeProfileSource(tx, id)
		if err != nil {
			return err
		}
		base, raw, err := webRuntimeBase(source.Plain)
		if err != nil {
			return err
		}
		switch parts[1] {
		case "base":
			result = base
		case "config":
			result = raw
		case "outbounds":
			proxies, err := outboundsOf(raw.Proxy, false)
			if err != nil {
				return err
			}
			groups, err := outboundsOf(raw.ProxyGroup, true)
			if err != nil {
				return err
			}
			result = &ProfileOutbounds{Proxies: proxies, ProxyGroups: groups, ProxyProviders: outboundMetadataNames(raw.ProxyProvider), RuleProviders: outboundMetadataNames(raw.RuleProvider), SubRules: outboundMetadataNames(raw.SubRules)}
		default:
			return webAPIError(404, "not_found", "profile derived resource does not exist")
		}
		return nil
	})
	if err != nil {
		WebAPIWriteError(w, webRuntimeFailure(err))
		return
	}
	rt.profileAnswer(w, r, revision, result)
}
