//go:build webpanel && !android

package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	bolt "github.com/metacubex/bbolt"
)

const webAPIStoreFile = "resources.db"
const webAPIMaxJSONBytes = 1 << 20
const webAPIMaxReceiptBytes = 48 << 20
const WebAPIMaxBulkJSONBytes = 32 << 20
const WebAPIMaxContentJSONBytes = 48 << 20
const WebAPIMaxPreviewJSONBytes = 72 << 20

var webAPIConfigBuckets = []string{"profiles", "rules", "disabled-rules", "profile-catalogs", "scripts", "proxy-groups", "custom-proxies", "proxy-dialers", "rule-providers", "mitm-modules", "icon-sets", "content", "settings"}
var webAPICacheBuckets = []string{"icon-records", "ip-qualities"}

type WebAPIStoreOptions struct {
	Now           func() time.Time
	DataDir       string
	StoreDir      string
	ManagedRoots  []string
	OnCommit      func(WebAPICommit)
	syncDirectory func(*os.File) error
}

type WebAPIStore struct {
	db          *bolt.DB
	root        *os.Root
	mu          sync.Mutex
	access      sync.RWMutex
	quarantined bool
	commit      func(*bolt.Tx) error
	onCommit    func(WebAPICommit)
	now         func() time.Time
	closed      bool
}

type WebAPIReadTx struct{ tx *bolt.Tx }
type WebAPIWriteTx struct {
	*WebAPIReadTx
	authority *webAPIWriteAuthority
}
type webAPIWriteAuthority struct {
	now             func() time.Time
	sessionID       string
	operationID     string
	revision        WebAPIRevision
	davRecorded     bool
	command         string
	repairTarget    WebAPIID
	repairedProfile WebAPIID
}
type WebAPICacheTx struct{ tx *bolt.Tx }

type webAPIStoredReceipt struct {
	Class       string          `json:"class,omitempty"`
	Hash        string          `json:"hash"`
	Revision    WebAPIRevision  `json:"revision"`
	OperationID string          `json:"operationId"`
	Body        json.RawMessage `json:"body"`
}

func webAPIPath(name string) (string, error) {
	if name == "" {
		return "", errors.New("directory is required")
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, e := os.Lstat(current)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		if e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return "", errors.New("directory chain must not contain links or files")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return absolute, nil
}

func webAPIPathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func webAPIOpenPrivateRoot(path string, syncDirectory func(*os.File) error) (*os.Root, error) {
	volume := filepath.VolumeName(path)
	root, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path[len(volume):], string(filepath.Separator)), string(filepath.Separator))
	for i, part := range parts {
		if part == "" {
			continue
		}
		info, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			if err = root.Mkdir(part, 0o700); err == nil {
				var directory *os.File
				directory, err = root.Open(".")
				if err == nil {
					err = syncDirectory(directory)
					directory.Close()
				}
				if err == nil {
					info, err = root.Lstat(part)
				}
			}
		}
		if err != nil {
			root.Close()
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (i == len(parts)-1 && info.Mode().Perm() != 0o700) {
			root.Close()
			return nil, errors.New("private store directory must be a non-link directory with mode 0700")
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			root.Close()
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			root.Close()
			return nil, errors.New("store directory changed while opening")
		}
		root.Close()
		root = next
	}
	return root, nil
}

func webAPIPrivateFile(info os.FileInfo) bool {
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return false
	}
	v := reflect.ValueOf(info.Sys())
	if v.IsValid() && v.Kind() == reflect.Pointer && !v.IsNil() {
		n := v.Elem().FieldByName("Nlink")
		if n.IsValid() && n.CanUint() && n.Uint() != 1 {
			return false
		}
	}
	return true
}

func OpenWebAPIStore(opts WebAPIStoreOptions) (*WebAPIStore, error) {
	data, err := webAPIPath(opts.DataDir)
	if err != nil {
		return nil, errors.New("invalid managed data directory")
	}
	storeName := opts.StoreDir
	if storeName == "" {
		storeName = filepath.Join(filepath.Dir(data), filepath.Base(data)+".api-store")
	}
	storePath, err := webAPIPath(storeName)
	if err != nil {
		return nil, errors.New("invalid private store directory")
	}
	for _, name := range append([]string{data}, opts.ManagedRoots...) {
		managed, err := webAPIPath(name)
		if err != nil {
			return nil, errors.New("invalid managed resource directory")
		}
		if webAPIPathWithin(managed, storePath) || webAPIPathWithin(storePath, managed) {
			return nil, errors.New("private store must not overlap managed resource directories")
		}
	}
	syncDirectory := opts.syncDirectory
	if syncDirectory == nil {
		syncDirectory = func(f *os.File) error { return f.Sync() }
	}
	root, err := webAPIOpenPrivateRoot(storePath, syncDirectory)
	if err != nil {
		return nil, errors.New("cannot open private store directory")
	}
	db, err := bolt.Open(filepath.Join(storePath, webAPIStoreFile), 0o600, &bolt.Options{
		Timeout: time.Second,
		OpenFile: func(_ string, flag int, _ os.FileMode) (*os.File, error) {
			before, err := root.Lstat(webAPIStoreFile)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			if err == nil && !webAPIPrivateFile(before) {
				return nil, errors.New("invalid private store file")
			}
			if errors.Is(err, os.ErrNotExist) {
				flag |= os.O_EXCL
			}
			f, err := root.OpenFile(webAPIStoreFile, flag, 0o600)
			if err != nil {
				return nil, err
			}
			after, statErr := f.Stat()
			nameInfo, nameErr := root.Lstat(webAPIStoreFile)
			if statErr != nil || nameErr != nil || !webAPIPrivateFile(after) || !os.SameFile(after, nameInfo) || (before != nil && !os.SameFile(before, after)) {
				f.Close()
				return nil, errors.New("private store file changed while opening")
			}
			return f, nil
		},
	})
	if err != nil {
		root.Close()
		return nil, errors.New("cannot open private store")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	s := &WebAPIStore{db: db, root: root, onCommit: opts.OnCommit, now: now, commit: func(tx *bolt.Tx) error { return tx.Commit() }}
	err = db.Update(func(tx *bolt.Tx) error {
		buckets := append(append([]string{}, webAPIConfigBuckets...), webAPICacheBuckets...)
		buckets = append(buckets, webAPISourceBuckets...)
		buckets = append(buckets, "dav-jobs")
		buckets = append(buckets, "meta", "receipts", "cache-meta", "cache-receipts", "operation-tombstones", "cache-operation-tombstones")
		for _, name := range buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		meta := tx.Bucket([]byte("meta"))
		version := meta.Get([]byte("schema"))
		if version != nil && !bytes.Equal(version, []byte("1")) {
			return errors.New("unsupported private store schema")
		}
		if meta.Get([]byte("store-id")) == nil {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return err
			}
			id[6] = (id[6] & 0x0f) | 0x40
			id[8] = (id[8] & 0x3f) | 0x80
			value := fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
			if err := meta.Put([]byte("store-id"), []byte(value)); err != nil {
				return err
			}
		}
		return meta.Put([]byte("schema"), []byte("1"))
	})
	if err != nil {
		s.Close()
		return nil, errors.New("cannot initialize private store")
	}
	f, err := root.Open(".")
	if err == nil {
		err = syncDirectory(f)
		f.Close()
	}
	if err != nil {
		s.Close()
		return nil, errors.New("cannot sync private store directory")
	}
	return s, nil
}

func (s *WebAPIStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access.Lock()
	defer s.access.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.db.Close(), s.root.Close())
}

func webAPIRevision(tx *bolt.Tx, bucket string) (WebAPIRevision, error) {
	v := tx.Bucket([]byte(bucket)).Get([]byte("revision"))
	if v == nil {
		return "0", nil
	}
	n, err := webAPIInteger(string(v), false)
	if err != nil || n < 0 {
		return "", errors.New("invalid stored revision")
	}
	return WebAPIRevision(v), nil
}

func webAPIOperationID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// The callback must only mutate this transaction. OnCommit may read but must not reenter writes.
func (s *WebAPIStore) Transaction(ctx context.Context, meta WebAPIMutation, command string, request any, apply func(*WebAPIWriteTx) (any, error)) (WebAPIReceipt, error) {
	authority := &webAPIWriteAuthority{now: s.now, sessionID: WebAPIRequestSessionID(ctx), operationID: meta.OperationID, command: command}
	if command == "profiles:reset-dialers" {
		if input, ok := request.(WebAPIProfileResetDialers); ok && input.WebAPIMutation == meta {
			authority.repairTarget = input.ID
		}
	}
	return s.transaction(ctx, meta, command, request, false, authority, func(tx *bolt.Tx) (any, error) {
		current, err := webAPIRevision(tx, "meta")
		if err != nil {
			return nil, err
		}
		authority.revision = current
		return apply(&WebAPIWriteTx{WebAPIReadTx: &WebAPIReadTx{tx}, authority: authority})
	})
}

func (s *WebAPIStore) CacheTransaction(ctx context.Context, meta WebAPIMutation, command string, request any, apply func(*WebAPICacheTx) (any, error)) (WebAPIReceipt, error) {
	return s.transaction(ctx, meta, command, request, true, nil, func(tx *bolt.Tx) (any, error) { return apply(&WebAPICacheTx{tx}) })
}

func (s *WebAPIStore) transaction(ctx context.Context, mutation WebAPIMutation, command string, request any, cache bool, authority *webAPIWriteAuthority, apply func(*bolt.Tx) (any, error)) (WebAPIReceipt, error) {
	var result WebAPIReceipt
	if !webAPIOperationID(mutation.OperationID) {
		return result, webAPIError(400, "invalid_operation_id", "operationId must contain 8–128 ASCII letters, digits, '-' or '_'")
	}
	expected, err := webAPIInteger(string(mutation.ExpectedRevision), false)
	if err != nil || expected < 0 {
		return result, webAPIError(400, "invalid_revision", "expectedRevision must be a decimal revision string")
	}
	body, err := json.Marshal(struct {
		Mutation WebAPIMutation `json:"mutation"`
		Request  any            `json:"request"`
	}{mutation, request})
	if err != nil {
		return result, webAPIError(400, "invalid_request", "request cannot be encoded")
	}
	hash := sha256.Sum256(append(append([]byte(command), 0), body...))
	fingerprint := hex.EncodeToString(hash[:])
	metaBucket, receiptBucket := "meta", "receipts"
	if cache {
		metaBucket, receiptBucket = "cache-meta", "cache-receipts"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return result, webAPIError(503, "store_closed", "store is closed")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	err = s.write(ctx, func(tx *bolt.Tx) error {
		revision, err := webAPIRevision(tx, metaBucket)
		if err != nil {
			return err
		}
		fail := func(e *WebAPIError) error { e.Revision = revision; return e }
		receipts := tx.Bucket([]byte(receiptBucket))
		if raw := receipts.Get([]byte(mutation.OperationID)); raw != nil {
			var saved webAPIStoredReceipt
			if err := json.Unmarshal(raw, &saved); err != nil {
				return err
			}
			if !cache {
				if err := webAPIAuthorizeDAVReceipt(ctx, tx, saved); err != nil {
					return err
				}
			}
			if saved.Hash != fingerprint {
				return fail(webAPIError(409, "operation_conflict", "operationId was used with a different command or body"))
			}
			result = WebAPIReceipt{Revision: saved.Revision, OperationID: saved.OperationID, Body: append(json.RawMessage{}, saved.Body...), Replayed: true}
			return nil
		}
		tombstoneBucket := "operation-tombstones"
		if cache {
			tombstoneBucket = "cache-operation-tombstones"
		}
		if tx.Bucket([]byte(tombstoneBucket)).Get([]byte(mutation.OperationID)) != nil {
			return fail(webAPINotCommitted())
		}
		if mutation.ExpectedRevision != revision {
			return fail(webAPIError(409, "revision_conflict", "expectedRevision does not match the committed revision"))
		}
		current, _ := strconv.ParseInt(string(revision), 10, 64)
		if current == math.MaxInt64 {
			return fail(webAPIError(409, "revision_exhausted", "revision space is exhausted"))
		}
		data, err := apply(tx)
		if err != nil {
			var apiErr *WebAPIError
			if errors.As(err, &apiErr) {
				return fail(apiErr)
			}
			return err
		}
		scopedRepair := !cache && authority != nil && command == "profiles:reset-dialers" && authority.repairTarget != "" && authority.repairedProfile == authority.repairTarget
		if !cache && !scopedRepair {
			if err := (&WebAPIReadTx{tx}).validateIntegrity(); err != nil {
				var problem *WebAPIError
				if errors.As(err, &problem) {
					return fail(problem)
				}
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		next := WebAPIRevision(strconv.FormatInt(current+1, 10))
		response, err := json.Marshal(WebAPIEnvelope[any]{Revision: next, Data: data, OperationID: mutation.OperationID})
		if err != nil {
			return err
		}
		if len(response) > webAPIMaxReceiptBytes {
			return fail(webAPIError(413, "response_too_large", "transaction response exceeds the receipt limit"))
		}
		class := ""
		if !cache && (strings.HasPrefix(command, "dav:") || authority != nil && authority.davRecorded) {
			class = "dav"
		}
		storedReceipt := webAPIStoredReceipt{Class: class, Hash: fingerprint, Revision: next, OperationID: mutation.OperationID, Body: response}
		if class == "dav" {
			if err := webAPIAuthorizeDAVReceipt(ctx, tx, storedReceipt); err != nil {
				return err
			}
		}
		stored, err := json.Marshal(storedReceipt)
		if err != nil {
			return err
		}
		if err = receipts.Put([]byte(mutation.OperationID), stored); err != nil {
			return err
		}
		if err = tx.Bucket([]byte(metaBucket)).Put([]byte("revision"), []byte(next)); err != nil {
			return err
		}
		result = WebAPIReceipt{Revision: next, OperationID: mutation.OperationID, Body: response}
		return nil
	})
	if err != nil {
		return WebAPIReceipt{}, err
	}
	if !cache && !result.Replayed && s.onCommit != nil {
		func() {
			defer func() { _ = recover() }()
			s.onCommit(WebAPICommit{Revision: result.Revision, OperationID: result.OperationID})
		}()
	}
	return result, nil
}

func (s *WebAPIStore) View(ctx context.Context, read func(*WebAPIReadTx) error) (WebAPIRevision, error) {
	var revision WebAPIRevision
	if err := ctx.Err(); err != nil {
		return revision, err
	}
	err := s.read(func(tx *bolt.Tx) error {
		var err error
		revision, err = webAPIRevision(tx, "meta")
		if err != nil {
			return err
		}
		return read(&WebAPIReadTx{tx})
	})
	return revision, err
}

func (s *WebAPIStore) Receipt(ctx context.Context, operationID string) (WebAPIReceipt, error) {
	return s.receipt(ctx, operationID, "receipts")
}

func (s *WebAPIStore) CacheReceipt(ctx context.Context, operationID string) (WebAPIReceipt, error) {
	return s.receipt(ctx, operationID, "cache-receipts")
}

func (s *WebAPIStore) receipt(ctx context.Context, operationID, bucket string) (WebAPIReceipt, error) {
	var result WebAPIReceipt
	if !webAPIOperationID(operationID) {
		return result, webAPIError(400, "invalid_operation_id", "invalid operationId")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	err := s.read(func(tx *bolt.Tx) error {
		var saved webAPIStoredReceipt
		tombstoneBucket := "operation-tombstones"
		if bucket == "cache-receipts" {
			tombstoneBucket = "cache-operation-tombstones"
		}
		if tx.Bucket([]byte(tombstoneBucket)).Get([]byte(operationID)) != nil {
			return webAPINotCommitted()
		}
		if err := webAPIGet(tx, bucket, operationID, &saved); err != nil {
			if bucket == "receipts" {
				if stateErr := webAPIMissingDAVReceipt(ctx, tx, operationID); stateErr != nil {
					return stateErr
				}
			}
			return err
		}
		if bucket == "receipts" && webAPIIsDAVReceipt(tx, saved) {
			release, err := WebAPIAcquireCommitAuthorization(ctx)
			if err != nil {
				return err
			}
			defer release()
			if err = webAPIAuthorizeDAVReceipt(ctx, tx, saved); err != nil {
				return err
			}
		}
		result = WebAPIReceipt{Revision: saved.Revision, OperationID: saved.OperationID, Body: append(json.RawMessage{}, saved.Body...), Replayed: true}
		return nil
	})
	return result, err
}

func webAPIGet(tx *bolt.Tx, bucket, key string, out any) error {
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return errors.New("missing store bucket")
	}
	raw := b.Get([]byte(key))
	if raw == nil {
		return webAPIError(404, "not_found", "resource does not exist")
	}
	return json.Unmarshal(raw, out)
}

func webAPIPut(tx *bolt.Tx, bucket, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return tx.Bucket([]byte(bucket)).Put([]byte(key), b)
}

func webAPIList[T any](tx *bolt.Tx, bucket string) ([]T, error) {
	items := make([]T, 0)
	err := tx.Bucket([]byte(bucket)).ForEach(func(_, raw []byte) error {
		var item T
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return items, err
}

func (tx *WebAPIWriteTx) AllocateID() (WebAPIID, error) {
	meta := tx.tx.Bucket([]byte("meta"))
	raw := meta.Get([]byte("last-id"))
	var last int64
	var err error
	if raw != nil {
		last, err = webAPIInteger(string(raw), false)
	}
	if err != nil || last < 0 {
		return "", errors.New("invalid identity counter")
	}
	if last == math.MaxInt64 {
		return "", webAPIError(409, "identity_exhausted", "identity space is exhausted")
	}
	id := WebAPIID(strconv.FormatInt(last+1, 10))
	return id, meta.Put([]byte("last-id"), []byte(id))
}

func (tx *WebAPIWriteTx) ReserveImportedID(id WebAPIID) error {
	n, err := webAPIInteger(string(id), true)
	if err != nil {
		return webAPIError(400, "invalid_id", "invalid imported identity")
	}
	meta := tx.tx.Bucket([]byte("meta"))
	last := int64(0)
	if raw := meta.Get([]byte("last-id")); raw != nil {
		last, err = webAPIInteger(string(raw), false)
		if err != nil {
			return err
		}
	}
	if n > last {
		return meta.Put([]byte("last-id"), []byte(id))
	}
	return nil
}

func (tx *WebAPIWriteTx) ResetConfiguration() error {
	for _, name := range webAPIConfigBuckets {
		if err := tx.tx.DeleteBucket([]byte(name)); err != nil {
			return err
		}
		if _, err := tx.tx.CreateBucket([]byte(name)); err != nil {
			return err
		}
	}
	return tx.ResetAllSourceGrants()
}

func webAPIContentKey(key WebAPIContentKey) (string, string, error) {
	if _, err := webAPIInteger(string(key.ID), true); err != nil {
		return "", "", webAPIError(400, "invalid_id", "invalid content owner")
	}
	bucket := map[WebAPIContentKind]string{WebAPIContentProfile: "profiles", WebAPIContentProfileAsset: "profiles", WebAPIContentScript: "scripts", WebAPIContentRuleProvider: "rule-providers", WebAPIContentMitmModule: "mitm-modules", WebAPIContentMitmResource: "mitm-modules"}[key.Kind]
	if bucket == "" {
		return "", "", webAPIError(400, "invalid_content_kind", "invalid content kind")
	}
	if key.Kind == WebAPIContentProfileAsset {
		if !webAPIProfileAssetName(key.Name) {
			return "", "", webAPIError(400, "invalid_content_name", "profile asset requires a typed planet digest name")
		}
	} else if key.Kind == WebAPIContentMitmResource {
		if key.Name == "" || len(key.Name) > 256 || strings.ContainsAny(key.Name, "/\\\x00\r\n") || key.Name == "." || key.Name == ".." {
			return "", "", webAPIError(400, "invalid_content_name", "invalid module resource name")
		}
	} else if key.Name != "" {
		return "", "", webAPIError(400, "invalid_content_name", "this content kind has no named children")
	}
	return string(key.Kind) + ":" + string(key.ID) + ":" + key.Name, bucket, nil
}

func (tx *WebAPIReadTx) Content(key WebAPIContentKey) (WebAPIContent, error) {
	var content WebAPIContent
	name, _, err := webAPIContentKey(key)
	if err != nil {
		return content, err
	}
	err = webAPIGet(tx.tx, "content", name, &content)
	return content, err
}

func (tx *WebAPIReadTx) Contents() ([]WebAPIContent, error) {
	return webAPIList[WebAPIContent](tx.tx, "content")
}

func (tx *WebAPIWriteTx) PutContent(content WebAPIContent) error {
	name, bucket, err := webAPIContentKey(content.Key)
	if err != nil {
		return err
	}
	if tx.tx.Bucket([]byte(bucket)).Get([]byte(content.Key.ID)) == nil {
		return webAPIError(409, "invalid_reference", "content owner does not exist")
	}
	if len(content.Bytes) > WebAPIMaxContentBytes {
		return webAPIError(413, "content_too_large", "content exceeds the resource limit")
	}
	if content.MediaType == "" || len(content.MediaType) > 128 || strings.ContainsAny(content.MediaType, "\r\n\x00") {
		return webAPIError(400, "invalid_media_type", "invalid content media type")
	}
	hash := sha256.Sum256(content.Bytes)
	content.SHA256 = hex.EncodeToString(hash[:])
	if content.Key.Kind == WebAPIContentProfileAsset {
		if content.Key.Name != "zerotier-planet:"+content.SHA256 {
			return webAPIError(409, "profile_asset_digest_mismatch", "profile asset identity does not match its source bytes")
		}
		raw, bytes, err := tx.profileAssetRows(content.Key.ID, name)
		if err != nil {
			return err
		}
		if len(raw) >= webAPIMaxProfileAssets || int64(len(content.Bytes)) > int64(WebAPIMaxContentBytes)-bytes {
			return webAPIError(413, "profile_asset_limit", "profile asset slots or aggregate source bytes exceed the limit")
		}
	}
	if tx.authority == nil || tx.authority.now == nil {
		return webAPIError(500, "content_clock_unavailable", "content writes require a store-owned transaction clock")
	}
	content.ModifiedAt = tx.authority.now().UTC().Format(time.RFC3339Nano)
	return webAPIPut(tx.tx, "content", name, content)
}

func (tx *WebAPIWriteTx) DeleteContent(key WebAPIContentKey) error {
	name, _, err := webAPIContentKey(key)
	if err != nil {
		return err
	}
	return tx.tx.Bucket([]byte("content")).Delete([]byte(name))
}

func (tx *WebAPICacheTx) IconRecords() ([]WebAPIIconRecord, error) {
	return webAPIList[WebAPIIconRecord](tx.tx, "icon-records")
}
func (tx *WebAPICacheTx) PutIconRecord(v WebAPIIconRecord) error {
	return webAPIPut(tx.tx, "icon-records", v.URL, v)
}
func (tx *WebAPICacheTx) DeleteIconRecord(url string) error {
	return tx.tx.Bucket([]byte("icon-records")).Delete([]byte(url))
}
func (tx *WebAPICacheTx) IPQualities() ([]WebAPIIPQualityRecord, error) {
	return webAPIList[WebAPIIPQualityRecord](tx.tx, "ip-qualities")
}
func (tx *WebAPICacheTx) PutIPQuality(v WebAPIIPQualityRecord) error {
	return webAPIPut(tx.tx, "ip-qualities", v.IP, v)
}
func (tx *WebAPICacheTx) DeleteIPQuality(ip string) error {
	return tx.tx.Bucket([]byte("ip-qualities")).Delete([]byte(ip))
}

func (s *WebAPIStore) CacheView(ctx context.Context, read func(*WebAPICacheTx) error) (WebAPIRevision, error) {
	var revision WebAPIRevision
	if err := ctx.Err(); err != nil {
		return revision, err
	}
	err := s.read(func(tx *bolt.Tx) error {
		var err error
		revision, err = webAPIRevision(tx, "cache-meta")
		if err != nil {
			return err
		}
		return read(&WebAPICacheTx{tx})
	})
	return revision, err
}

func webAPIOrder(index int) string { return fmt.Sprintf("%016x", index) }

func (s *WebAPIStore) write(ctx context.Context, apply func(*bolt.Tx) error) error {
	s.access.Lock()
	defer s.access.Unlock()
	if s.closed {
		return webAPIError(503, "store_closed", "store is closed")
	}
	if s.quarantined {
		return webAPIError(503, "durability_unknown", "store must be reopened before reconciling receipts")
	}
	tx, err := s.db.Begin(true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	release, err := WebAPIAcquireCommitAuthorization(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err = apply(tx); err != nil {
		return err
	}
	if err = s.commit(tx); err != nil {
		s.quarantined = true
		return webAPIError(503, "durability_unknown", "commit durability is unknown; reopen store and reconcile the operation receipt")
	}
	return nil
}
func (s *WebAPIStore) read(read func(*bolt.Tx) error) error {
	s.access.RLock()
	defer s.access.RUnlock()
	if s.closed {
		return webAPIError(503, "store_closed", "store is closed")
	}
	if s.quarantined {
		return webAPIError(503, "durability_unknown", "store must be reopened before reconciling receipts")
	}
	return s.db.View(read)
}
func (tx *WebAPIReadTx) Meta() (WebAPIStoreMeta, error) {
	revision, err := webAPIRevision(tx.tx, "meta")
	if err != nil {
		return WebAPIStoreMeta{}, err
	}
	return WebAPIStoreMeta{StoreID: string(tx.tx.Bucket([]byte("meta")).Get([]byte("store-id"))), SchemaVersion: 1, Revision: revision}, nil
}
