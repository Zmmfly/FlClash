//go:build webpanel && !android

package core

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	bolt "github.com/metacubex/bbolt"
	"github.com/metacubex/mihomo/component/geodata"
	"github.com/metacubex/mihomo/component/mmdb"
	C "github.com/metacubex/mihomo/constant"
	"github.com/oschwald/maxminddb-golang"
)

const webRuntimeGeoBucket = "runtime-geo-sources"
const WebAPIMaxGeoBytes = WebAPIMaxGeoSourceBytes

type webRuntimeGeoDescriptor struct {
	ID   WebAPIRuntimeProviderID `json:"id"`
	Type string                  `json:"type"`
	URL  string                  `json:"url"`
}
type webRuntimeDesiredGeo struct {
	Request        WebAPIRuntimeGeoRequest `json:"request"`
	Descriptor     webRuntimeGeoDescriptor `json:"descriptor"`
	SchedulerEpoch *string                 `json:"schedulerEpoch,omitempty"`
}

func webRuntimeGeoDescriptors(tx *WebAPIReadTx, storeID string) ([]webRuntimeGeoDescriptor, error) {
	settings, err := tx.Settings()
	if err != nil {
		return nil, err
	}
	urls := settings.Runtime.PatchClashConfig.GeoXURL
	out := []webRuntimeGeoDescriptor{}
	for _, item := range []struct{ kind, url string }{{"MMDB", urls.MMDB}, {"ASN", urls.ASN}, {"GEOIP", urls.GEOIP}, {"GEOSITE", urls.GEOSITE}} {
		if _, err := webContentURL(item.url); err != nil {
			return nil, err
		}
		id := webRuntimeOpaqueID(struct {
			Store string
			Kind  string
			URL   string
		}{storeID, "geo:" + item.kind, item.url})
		out = append(out, webRuntimeGeoDescriptor{ID: id, Type: item.kind, URL: item.url})
	}
	return out, nil
}

func webRuntimeGeoByID(tx *WebAPIReadTx, storeID string, id WebAPIRuntimeProviderID) (webRuntimeGeoDescriptor, error) {
	values, err := webRuntimeGeoDescriptors(tx, storeID)
	if err != nil {
		return webRuntimeGeoDescriptor{}, err
	}
	for _, value := range values {
		if value.ID == id {
			return value, nil
		}
	}
	return webRuntimeGeoDescriptor{}, webAPIError(409, "source_target_changed", "geo source changed; reload its authorization")
}

func (rt *WebAPIRuntime) geoHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	var data []WebAPIRuntimeGeoResource
	rev, err := rt.store.View(r.Context(), func(tx *WebAPIReadTx) error {
		sources, err := webRuntimeGeoDescriptors(tx, rt.Events.storeID)
		if err != nil {
			return err
		}
		for _, source := range sources {
			data = append(data, WebAPIRuntimeGeoResource{Type: source.Type, Updating: webRuntimeGeoUpdating(source.Type), SourceID: source.ID})
		}
		return nil
	})
	if err != nil {
		WebAPIWriteError(w, err)
		return
	}
	if rt.backend.GeoMetadata != nil {
		for i := range data {
			metadata, err := rt.backend.GeoMetadata(r.Context(), data[i].Type)
			if err != nil {
				WebAPIWriteError(w, webRuntimeFailure(err))
				return
			}
			data[i].Available, data[i].Size, data[i].ModifiedAt = metadata.Available, metadata.Size, metadata.ModifiedAt
		}
	}
	rt.profileAnswer(w, r, rev, data)
}

func webRuntimeReadGeoMetadata(ctx context.Context, kind string) (WebAPIRuntimeGeoMetadata, error) {
	var result WebAPIRuntimeGeoMetadata
	if err := ctx.Err(); err != nil {
		return result, err
	}
	home := filepath.Clean(C.Path.HomeDir())
	name := ""
	switch kind {
	case "MMDB":
		name = C.Path.MMDB()
	case "ASN":
		name = C.Path.ASN()
	case "GEOIP":
		name = C.Path.GeoIP()
	case "GEOSITE":
		name = C.Path.GeoSite()
	default:
		return result, webAPIError(400, "invalid_geo_resource", "unknown geo resource")
	}
	if filepath.Dir(name) != home {
		return result, webAPIError(409, "invalid_geo_destination", "geo metadata is outside the server-owned resource root")
	}
	root, err := os.OpenRoot(home)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, webAPIError(503, "geo_metadata_unavailable", "server geo metadata is unavailable")
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(name))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, webAPIError(503, "geo_metadata_unavailable", "server geo metadata is unavailable")
	}
	if !info.Mode().IsRegular() {
		return result, nil
	}
	size, modified := info.Size(), info.ModTime().UTC()
	result.Available, result.Size, result.ModifiedAt = true, &size, &modified
	return result, nil
}

func (rt *WebAPIRuntime) geoUpdateHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := WebAPIQuery(r); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	var p WebAPIRuntimeGeoRequest
	if err := WebAPIDecodeJSON(w, r, &p); err != nil {
		WebAPIWriteError(w, err)
		return
	}
	const command = "runtime/geo:update"
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
	if rt.backend.InstallGeo == nil {
		WebAPIWriteError(w, webRuntimeUnavailable())
		return
	}
	retained := context.WithoutCancel(r.Context())
	_, existed := rt.providers.sessions.LoadOrStore(p.OperationID, retained)
	receipt, err := rt.store.Transaction(r.Context(), p.WebAPIMutation, command, p, func(tx *WebAPIWriteTx) (any, error) {
		values, err := webRuntimeGeoDescriptors(tx.WebAPIReadTx, rt.Events.storeID)
		if err != nil {
			return nil, err
		}
		for _, d := range values {
			if d.Type == p.Type {
				if p.SourceID != "" && p.SourceID != d.ID {
					return nil, webAPIError(409, "source_target_changed", "geo source changed before acceptance")
				}
				if _, err = tx.tx.CreateBucketIfNotExists([]byte(webRuntimeGeoBucket)); err != nil {
					return nil, err
				}
				if err = webAPIPut(tx.tx, webRuntimeGeoBucket, string(d.ID), d); err != nil {
					return nil, err
				}
				return tx.RecordRuntimeIntent(p.OperationID, webRuntimeGeoUpdate, webRuntimeDesiredGeo{Request: p, Descriptor: d})
			}
		}
		return nil, webAPIError(400, "invalid_geo_resource", "unknown geo resource")
	})
	if err != nil {
		if !existed {
			rt.providers.sessions.Delete(p.OperationID)
		}
		WebAPIWriteError(w, err)
		return
	}
	if !receipt.Replayed {
		select {
		case rt.wake <- struct{}{}:
		default:
		}
	} else if !existed {
		rt.providers.sessions.Delete(p.OperationID)
	}
	WebAPIWriteReceipt(w, receipt)
}

func (rt *WebAPIRuntime) geoUpdate(ctx context.Context, desired webRuntimeDesiredGeo) (result any, err error) {
	defer rt.providers.sessions.Delete(desired.Request.OperationID)
	original, ok := rt.providers.sessions.Load(desired.Request.OperationID)
	if !ok {
		return nil, webAPIError(401, "session_unavailable", "geo refresh has no live authorization context")
	}
	ctx = webRuntimeRestoreAuthorization(ctx, original.(context.Context))
	release, err := WebAPIAcquireCommitAuthorization(ctx)
	if err != nil {
		return nil, err
	}
	release()
	if !claimGeoUpdate(desired.Descriptor.Type) {
		return nil, webAPIError(409, "geo_busy", "geo update is already in progress")
	}
	defer releaseGeoUpdate(desired.Descriptor.Type)
	rt.Events.publish(WebAPIEvent{Type: "geo", Data: GeoUpdateStatus{Type: desired.Descriptor.Type, Updating: true}}, "state")
	defer func() {
		status := GeoUpdateStatus{Type: desired.Descriptor.Type}
		if err != nil {
			status.Error = "geo update did not complete"
		}
		rt.Events.publish(WebAPIEvent{Type: "geo", Data: status}, "state")
	}()
	_, err = rt.store.View(ctx, func(tx *WebAPIReadTx) error {
		if err := webRuntimeCheckBackgroundEpoch(tx, desired.SchedulerEpoch); err != nil {
			return err
		}
		_, err := webRuntimeGeoByID(tx, rt.Events.storeID, desired.Descriptor.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	fetched, err := WebAPIFetchSource(ctx, rt.store, WebAPISourceRequest{Purpose: WebAPISourceGeoRefresh, Key: &WebAPISourceResource{Kind: "geo", ID: string(desired.Descriptor.ID)}, URL: desired.Descriptor.URL, AuthorizationID: desired.Request.AuthorizationID}, rt.workspace.options.SourceFetchOptions)
	if err != nil {
		return nil, err
	}
	data, err := webRuntimeGeoDecompress(fetched.Bytes)
	if err != nil {
		return nil, err
	}
	if err = webRuntimeValidateGeo(desired.Descriptor.Type, data); err != nil {
		return nil, err
	}
	release, err = WebAPIAcquireCommitAuthorization(ctx)
	if err != nil {
		return nil, err
	}
	release()
	_, err = rt.store.View(ctx, func(tx *WebAPIReadTx) error {
		if err := webRuntimeCheckBackgroundEpoch(tx, desired.SchedulerEpoch); err != nil {
			return err
		}
		_, err := webRuntimeGeoByID(tx, rt.Events.storeID, desired.Descriptor.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	err = func() error {
		rt.backendMu.Lock()
		defer rt.backendMu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := rt.store.View(ctx, func(tx *WebAPIReadTx) error {
			if err := webRuntimeCheckBackgroundEpoch(tx, desired.SchedulerEpoch); err != nil {
				return err
			}
			_, err := webRuntimeGeoByID(tx, rt.Events.storeID, desired.Descriptor.ID)
			return err
		})
		if err != nil {
			return err
		}
		return rt.backend.InstallGeo(ctx, desired.Descriptor.Type, data)
	}()
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": desired.Descriptor.Type, "sourceId": desired.Descriptor.ID, "installed": true}, nil
}

func webRuntimeGeoDecompress(data []byte) ([]byte, error) {
	if len(data) > WebAPIMaxGeoBytes {
		return nil, webAPIError(413, "geo_content_too_large", "geo content exceeds 128 MiB")
	}
	var reader io.ReadCloser
	var err error
	switch {
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		reader, err = gzip.NewReader(bytes.NewReader(data))
	case len(data) > 1 && data[0] == 0x78 && (uint16(data[0])<<8|uint16(data[1]))%31 == 0:
		reader, err = zlib.NewReader(bytes.NewReader(data))
	default:
		return data, nil
	}
	if err != nil {
		return nil, webAPIError(400, "invalid_compressed_content", "geo content has invalid compression")
	}
	defer reader.Close()
	return webContentReadLimit(reader, WebAPIMaxGeoBytes)
}

func webRuntimeValidateGeo(kind string, data []byte) error {
	if len(data) == 0 || len(data) > WebAPIMaxGeoBytes {
		return webAPIError(413, "invalid_geo_content", "geo content is empty or exceeds the resource budget")
	}
	switch kind {
	case "MMDB", "ASN":
		reader, err := maxminddb.FromBytes(data)
		if err != nil {
			return webAPIError(422, "invalid_geo_content", "source is not a valid MMDB database")
		}
		return reader.Close()
	case "GEOIP", "GEOSITE":
		loader, err := geodata.GetGeoDataLoader("standard")
		if err != nil {
			return webAPIError(422, "invalid_geo_content", "geo parser is unavailable")
		}
		if kind == "GEOIP" {
			_, err = loader.LoadIPByBytes(data, "cn")
		} else {
			_, err = loader.LoadSiteByBytes(data, "cn")
		}
		if err != nil {
			return webAPIError(422, "invalid_geo_content", "source is not a valid geo database")
		}
		return nil
	default:
		return webAPIError(400, "invalid_geo_resource", "unknown geo resource")
	}
}

func webRuntimeInstallGeo(ctx context.Context, kind string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := webRuntimeValidateGeo(kind, data); err != nil {
		return err
	}
	home := filepath.Clean(C.Path.HomeDir())
	name := ""
	switch kind {
	case "MMDB":
		name = C.Path.MMDB()
	case "ASN":
		name = C.Path.ASN()
	case "GEOIP":
		name = C.Path.GeoIP()
	case "GEOSITE":
		name = C.Path.GeoSite()
	default:
		return webAPIError(400, "invalid_geo_resource", "unknown geo resource")
	}
	if filepath.Dir(name) != home {
		return webAPIError(409, "invalid_geo_destination", "geo destination is not inside the server-owned runtime home")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return webAPIError(503, "geo_install_failed", "runtime home is unavailable")
	}
	defer root.Close()
	destination := filepath.Base(name)
	if info, err := root.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return webAPIError(409, "invalid_geo_destination", "geo destination is not a regular server resource")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	temporary := ".geo-" + hex.EncodeToString(random[:])
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, err = file.Write(data)
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		return webAPIError(503, "geo_install_failed", "geo content could not be staged")
	}
	if kind == "MMDB" {
		defer mmdb.ReloadIP()
	}
	if kind == "ASN" {
		defer mmdb.ReloadASN()
	}
	if kind == "MMDB" && mmdb.Verify(name) {
		reader := mmdb.IPInstance()
		if reader.Reader != nil {
			reader.Reader.Close()
		}
	}
	if kind == "ASN" && mmdb.Verify(name) {
		reader := mmdb.ASNInstance()
		if reader.Reader != nil {
			reader.Reader.Close()
		}
	}
	if err = root.Rename(temporary, destination); err != nil {
		return webAPIError(503, "geo_install_failed", "geo content could not be installed")
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	err = errors.Join(directory.Sync(), directory.Close())
	if err != nil {
		return webAPIError(503, "effect_unknown", "geo installation durability is unknown")
	}
	switch kind {
	case "GEOIP":
		geodata.ClearGeoIPCache()
	case "GEOSITE":
		geodata.ClearGeoSiteCache()
	}
	changeRoute(routeMoved)
	return nil
}

func (rt *WebAPIRuntime) sweepGeoSources(ctx context.Context) error {
	return rt.store.updateRuntime(ctx, func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(webRuntimeGeoBucket))
		if bucket == nil {
			return nil
		}
		current, err := webRuntimeGeoDescriptors(&WebAPIReadTx{tx}, rt.Events.storeID)
		if err != nil {
			return err
		}
		valid := map[string]bool{}
		for _, d := range current {
			valid[string(d.ID)] = true
		}
		invalid := []string{}
		if err = bucket.ForEach(func(id, _ []byte) error {
			if !valid[string(id)] {
				invalid = append(invalid, string(id))
			}
			return nil
		}); err != nil {
			return err
		}
		write := &WebAPIWriteTx{WebAPIReadTx: &WebAPIReadTx{tx}}
		for _, id := range invalid {
			if err = write.InvalidateSourceGrantsForResource(WebAPISourceResource{Kind: "geo", ID: id}); err != nil {
				return err
			}
			if err = bucket.Delete([]byte(id)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (rt *WebAPIRuntime) ensureGeoReady(ctx context.Context) error {
	if rt.backend.State == nil {
		return webRuntimeUnavailable()
	}
	state, err := rt.backend.State(ctx)
	if err != nil {
		return err
	}
	if !state.Initialized {
		return webAPIError(503, "core_unavailable", "server runtime is not initialized")
	}
	home := filepath.Clean(rt.workspace.options.WorkspaceRoot)
	if rt.workspace.options.WorkspaceRoot == "" || home != filepath.Clean(C.Path.HomeDir()) {
		return webAPIError(409, "geo_source_unavailable", "native runtime must use its server-owned resource root")
	}
	if !rt.backendMu.TryRLock() {
		return webAPIError(409, "runtime_transition_pending", "wait for the in-flight runtime effect before preparing another native apply")
	}
	defer rt.backendMu.RUnlock()
	root, err := os.OpenRoot(home)
	if err != nil {
		return webAPIError(409, "geo_source_unavailable", "prepare the server geo resources before applying")
	}
	defer root.Close()
	for _, kind := range []string{"MMDB", "ASN", "GEOIP", "GEOSITE"} {
		name := ""
		switch kind {
		case "MMDB":
			name = C.Path.MMDB()
		case "ASN":
			name = C.Path.ASN()
		case "GEOIP":
			name = C.Path.GeoIP()
		case "GEOSITE":
			name = C.Path.GeoSite()
		}
		if filepath.Dir(name) != home {
			return webAPIError(409, "geo_source_unavailable", "geo resources must remain inside the server runtime root")
		}
		base := filepath.Base(name)
		info, err := root.Lstat(base)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > WebAPIMaxGeoBytes {
			return webAPIError(409, "geo_source_unavailable", "prepare all server geo resources before native apply; native auto-download is not permitted")
		}
		file, err := root.Open(base)
		if err != nil {
			return webAPIError(409, "geo_source_unavailable", "server geo resource is unavailable")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, WebAPIMaxGeoBytes+1))
		file.Close()
		if readErr != nil || int64(len(data)) != info.Size() {
			return webAPIError(409, "geo_source_unavailable", "server geo resource changed while validating")
		}
		if len(data) > WebAPIMaxGeoBytes {
			return webAPIError(413, "geo_content_too_large", "server geo resource exceeds 128 MiB")
		}
		if err = webRuntimeValidateGeo(kind, data); err != nil {
			return webAPIError(409, "geo_source_unavailable", "server geo resource is not validated; prepare it through the typed geo command")
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}
