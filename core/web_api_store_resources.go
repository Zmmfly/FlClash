//go:build webpanel && !android

package core

func (tx *WebAPIReadTx) Scripts() ([]WebAPIScript, error) {
	return webAPIList[WebAPIScript](tx.tx, "scripts")
}
func (tx *WebAPIReadTx) Script(id WebAPIID) (WebAPIScript, error) {
	var v WebAPIScript
	err := webAPIGet(tx.tx, "scripts", string(id), &v)
	return v, err
}
func (tx *WebAPIWriteTx) InsertScript(v WebAPIScript) error {
	return tx.insertResource("scripts", v.ID, v)
}
func (tx *WebAPIWriteTx) SaveScript(v WebAPIScript) error { return tx.saveResource("scripts", v.ID, v) }
func (tx *WebAPIWriteTx) DeleteScript(id WebAPIID) error  { return tx.deleteResource("scripts", id) }

func (tx *WebAPIReadTx) ProxyGroups() ([]WebAPIProxyGroup, error) {
	return webAPIList[WebAPIProxyGroup](tx.tx, "proxy-groups")
}
func (tx *WebAPIReadTx) ProxyGroup(id WebAPIID) (WebAPIProxyGroup, error) {
	var v WebAPIProxyGroup
	err := webAPIGet(tx.tx, "proxy-groups", string(id), &v)
	return v, err
}
func (tx *WebAPIWriteTx) InsertProxyGroup(v WebAPIProxyGroup) error {
	return tx.insertResource("proxy-groups", v.ID, v)
}
func (tx *WebAPIWriteTx) SaveProxyGroup(v WebAPIProxyGroup) error {
	return tx.saveResource("proxy-groups", v.ID, v)
}
func (tx *WebAPIWriteTx) DeleteProxyGroup(id WebAPIID) error {
	return tx.deleteResource("proxy-groups", id)
}

func (tx *WebAPIReadTx) CustomProxies() ([]WebAPICustomProxy, error) {
	return webAPIList[WebAPICustomProxy](tx.tx, "custom-proxies")
}
func (tx *WebAPIReadTx) CustomProxy(id WebAPIID) (WebAPICustomProxy, error) {
	var v WebAPICustomProxy
	err := webAPIGet(tx.tx, "custom-proxies", string(id), &v)
	return v, err
}
func (tx *WebAPIWriteTx) InsertCustomProxy(v WebAPICustomProxy) error {
	return tx.insertResource("custom-proxies", v.ID, v)
}
func (tx *WebAPIWriteTx) SaveCustomProxy(v WebAPICustomProxy) error {
	return tx.saveResource("custom-proxies", v.ID, v)
}
func (tx *WebAPIWriteTx) DeleteCustomProxy(id WebAPIID) error {
	return tx.deleteResource("custom-proxies", id)
}

func (tx *WebAPIReadTx) ClashProviders() ([]WebAPIClashProvider, error) {
	return webAPIList[WebAPIClashProvider](tx.tx, "rule-providers")
}
func (tx *WebAPIReadTx) ClashProvider(id WebAPIID) (WebAPIClashProvider, error) {
	var v WebAPIClashProvider
	err := webAPIGet(tx.tx, "rule-providers", string(id), &v)
	return v, err
}
func (tx *WebAPIWriteTx) InsertClashProvider(v WebAPIClashProvider) error {
	return tx.insertResource("rule-providers", v.ID, v)
}
func (tx *WebAPIWriteTx) SaveClashProvider(v WebAPIClashProvider) error {
	return tx.saveResource("rule-providers", v.ID, v)
}
func (tx *WebAPIWriteTx) DeleteClashProvider(id WebAPIID) error {
	return tx.deleteResource("rule-providers", id)
}

func (tx *WebAPIReadTx) MitmModules() ([]WebAPIMitmModule, error) {
	return webAPIList[WebAPIMitmModule](tx.tx, "mitm-modules")
}
func (tx *WebAPIReadTx) MitmModule(id WebAPIID) (WebAPIMitmModule, error) {
	var v WebAPIMitmModule
	err := webAPIGet(tx.tx, "mitm-modules", string(id), &v)
	return v, err
}
func (tx *WebAPIWriteTx) InsertMitmModule(v WebAPIMitmModule) error {
	return tx.insertResource("mitm-modules", v.ID, v)
}
func (tx *WebAPIWriteTx) SaveMitmModule(v WebAPIMitmModule) error {
	return tx.saveResource("mitm-modules", v.ID, v)
}
func (tx *WebAPIWriteTx) DeleteMitmModule(id WebAPIID) error {
	return tx.deleteResource("mitm-modules", id)
}

func (tx *WebAPIReadTx) IconSets() ([]WebAPIIconSet, error) {
	return webAPIList[WebAPIIconSet](tx.tx, "icon-sets")
}
func (tx *WebAPIReadTx) IconSet(id WebAPIID) (WebAPIIconSet, error) {
	var v WebAPIIconSet
	err := webAPIGet(tx.tx, "icon-sets", string(id), &v)
	return v, err
}
func (tx *WebAPIWriteTx) InsertIconSet(v WebAPIIconSet) error {
	return tx.insertResource("icon-sets", v.ID, v)
}
func (tx *WebAPIWriteTx) SaveIconSet(v WebAPIIconSet) error {
	return tx.saveResource("icon-sets", v.ID, v)
}
func (tx *WebAPIWriteTx) DeleteIconSet(id WebAPIID) error { return tx.deleteResource("icon-sets", id) }

func (tx *WebAPIWriteTx) insertResource(bucket string, id WebAPIID, v any) error {
	if _, err := webAPIInteger(string(id), true); err != nil {
		return webAPIError(400, "invalid_id", "invalid resource identity")
	}
	if tx.tx.Bucket([]byte(bucket)).Get([]byte(id)) != nil {
		return webAPIError(409, "duplicate_id", "create cannot overwrite an existing resource")
	}
	if err := tx.ReserveImportedID(id); err != nil {
		return err
	}
	return webAPIPut(tx.tx, bucket, string(id), v)
}
func (tx *WebAPIWriteTx) saveResource(bucket string, id WebAPIID, v any) error {
	if tx.tx.Bucket([]byte(bucket)).Get([]byte(id)) == nil {
		return webAPIError(404, "not_found", "resource does not exist")
	}
	if err := tx.invalidateChangedSource(bucket, id, v); err != nil {
		return err
	}
	return webAPIPut(tx.tx, bucket, string(id), v)
}
func (tx *WebAPIWriteTx) deleteResource(bucket string, id WebAPIID) error {
	if tx.tx.Bucket([]byte(bucket)).Get([]byte(id)) == nil {
		return webAPIError(404, "not_found", "resource does not exist")
	}
	if err := tx.invalidateSourceResource(bucket, id); err != nil {
		return err
	}
	return tx.tx.Bucket([]byte(bucket)).Delete([]byte(id))
}
func (tx *WebAPIReadTx) ProxyDialers() ([]WebAPIProxyDialer, error) {
	return webAPIList[WebAPIProxyDialer](tx.tx, "proxy-dialers")
}
func (tx *WebAPIWriteTx) SaveProxyDialer(v WebAPIProxyDialer) error {
	return webAPIPut(tx.tx, "proxy-dialers", string(v.ProfileID)+":"+string(v.ProxyID), v)
}
func (tx *WebAPIWriteTx) DeleteProxyDialer(profileID, proxyID WebAPIID) error {
	return tx.tx.Bucket([]byte("proxy-dialers")).Delete([]byte(string(profileID) + ":" + string(proxyID)))
}
