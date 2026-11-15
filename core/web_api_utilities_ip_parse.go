//go:build webpanel && !android

package core

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

const webUtilityIPBodyLimit = 256 << 10

var webUtilityIPSources = []string{"identMe", "ipApiCom", "ipQuery", "ipLocate", "proxyCheck", "ipApiIs"}
var webUtilityIPASNNumber = regexp.MustCompile(`[0-9]+`)

type webUtilityIPASN struct{ value *uint32 }

func (a *webUtilityIPASN) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	text := string(data)
	if strings.HasPrefix(text, `"`) {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		text = webUtilityIPASNNumber.FindString(value)
		if text == "" {
			return nil
		}
	}
	number, err := strconv.ParseUint(text, 10, 32)
	if err != nil {
		return err
	}
	if number != 0 {
		v := uint32(number)
		a.value = &v
	}
	return nil
}

type webUtilityIPAnswer struct {
	quality  WebAPIIPQuality
	inferred bool
}

type WebAPIUtilityIPFailure struct {
	Source string `json:"source"`
	Status string `json:"status"`
}

type webUtilityIPFailureError struct{ status string }

func (e webUtilityIPFailureError) Error() string { return e.status }

func webUtilityIPURL(source, ip string) (string, error) {
	parsed, err := netip.ParseAddr(ip)
	if err != nil || parsed.Zone() != "" || parsed.String() != ip {
		return "", webAPIError(400, "invalid_ip", "expected a canonical IPv4 or IPv6 address without zone")
	}
	switch source {
	case "identMe":
		return "https://ident.me/json", nil
	case "ipApiCom":
		return "http://ip-api.com/json/" + ip + "?fields=status,isp,org,as,mobile,proxy,hosting", nil
	case "ipQuery":
		return "https://api.ipquery.io/" + ip + "?format=json", nil
	case "ipLocate":
		return "https://iplocate.io/api/lookup/" + ip, nil
	case "proxyCheck":
		return "https://proxycheck.io/v3/" + ip, nil
	case "ipApiIs":
		return "https://api.ipapi.is/?q=" + ip, nil
	}
	return "", webAPIError(400, "invalid_quality_source", "unknown IP quality source")
}

func webUtilityIPType(declared string) string {
	switch declared {
	case "isp":
		return "residential"
	case "business", "education", "government", "banking":
		return "business"
	case "hosting":
		return "hosting"
	}
	return ""
}

func webUtilityIPPick(hosting, mobile bool, declared string) string {
	if hosting || declared == "hosting" {
		return "hosting"
	}
	if mobile {
		return "mobile"
	}
	return declared
}

func webUtilityIPText(value, fallback string) *string {
	if value == "" {
		value = fallback
	}
	if value == "" {
		return nil
	}
	return &value
}

func webUtilityIPMatch(wanted, observed string) error {
	if observed != "" && observed != wanted {
		return webUtilityIPFailureError{"ipMismatch"}
	}
	return nil
}

func webUtilityParseIP(source, ip string, data []byte) (webUtilityIPAnswer, error) {
	answer := webUtilityIPAnswer{quality: WebAPIIPQuality{IP: ip, Source: source}}
	q := &answer.quality
	decode := func(target any) error { return webUtilityJSON(data, target, webUtilityIPBodyLimit) }
	switch source {
	case "identMe":
		var v struct {
			IP   string          `json:"ip"`
			Type string          `json:"type"`
			ASO  string          `json:"aso"`
			ASN  webUtilityIPASN `json:"asn"`
		}
		if err := decode(&v); err != nil {
			return answer, err
		}
		if v.IP != ip {
			return answer, webUtilityIPFailureError{"ipMismatch"}
		}
		q.Type, q.Organization, q.ASN = webUtilityIPType(v.Type), webUtilityIPText(v.ASO, ""), v.ASN.value
	case "ipApiCom":
		var v struct {
			Status  string          `json:"status"`
			Query   string          `json:"query"`
			ISP     string          `json:"isp"`
			Org     string          `json:"org"`
			AS      webUtilityIPASN `json:"as"`
			Mobile  bool            `json:"mobile"`
			Proxy   bool            `json:"proxy"`
			Hosting bool            `json:"hosting"`
		}
		if err := decode(&v); err != nil {
			return answer, err
		}
		if v.Status != "success" {
			return answer, webUtilityIPFailureError{"failed"}
		}
		if err := webUtilityIPMatch(ip, v.Query); err != nil {
			return answer, err
		}
		q.Type = webUtilityIPPick(v.Hosting, v.Mobile, "")
		answer.inferred = q.Type == ""
		if answer.inferred {
			q.Type = "residential"
		}
		q.Organization, q.ASN, q.IsProxy = webUtilityIPText(v.Org, v.ISP), v.AS.value, v.Proxy
	case "ipQuery":
		var v struct {
			IP  string `json:"ip"`
			ISP struct {
				Org string          `json:"org"`
				ISP string          `json:"isp"`
				ASN webUtilityIPASN `json:"asn"`
			} `json:"isp"`
			Risk struct {
				Datacenter bool `json:"is_datacenter"`
				Mobile     bool `json:"is_mobile"`
				Proxy      bool `json:"is_proxy"`
				VPN        bool `json:"is_vpn"`
				Tor        bool `json:"is_tor"`
			} `json:"risk"`
		}
		if err := decode(&v); err != nil {
			return answer, err
		}
		if err := webUtilityIPMatch(ip, v.IP); err != nil {
			return answer, err
		}
		q.Type = webUtilityIPPick(v.Risk.Datacenter, v.Risk.Mobile, "")
		answer.inferred = q.Type == ""
		if answer.inferred {
			q.Type = "residential"
		}
		q.Organization, q.ASN = webUtilityIPText(v.ISP.Org, v.ISP.ISP), v.ISP.ASN.value
		q.IsProxy, q.IsVPN, q.IsTor = v.Risk.Proxy, v.Risk.VPN, v.Risk.Tor
	case "ipLocate":
		var v struct {
			IP      string                      `json:"ip"`
			Company struct{ Type, Name string } `json:"company"`
			ASN     struct {
				Type string          `json:"type"`
				Name string          `json:"name"`
				ASN  webUtilityIPASN `json:"asn"`
			} `json:"asn"`
			Privacy struct {
				Hosting bool `json:"is_hosting"`
				Proxy   bool `json:"is_proxy"`
				VPN     bool `json:"is_vpn"`
				Tor     bool `json:"is_tor"`
				Abuser  bool `json:"is_abuser"`
			} `json:"privacy"`
		}
		if err := decode(&v); err != nil {
			return answer, err
		}
		if err := webUtilityIPMatch(ip, v.IP); err != nil {
			return answer, err
		}
		declared := webUtilityIPType(v.Company.Type)
		if declared == "" {
			declared = webUtilityIPType(v.ASN.Type)
		}
		q.Type, q.Organization, q.ASN = webUtilityIPPick(v.Privacy.Hosting, false, declared), webUtilityIPText(v.Company.Name, v.ASN.Name), v.ASN.ASN.value
		q.IsProxy, q.IsVPN, q.IsTor, q.IsAbuser = v.Privacy.Proxy, v.Privacy.VPN, v.Privacy.Tor, v.Privacy.Abuser
	case "proxyCheck":
		var status struct {
			Status string `json:"status"`
		}
		var entries map[string]json.RawMessage
		if err := decode(&status); err != nil {
			return answer, err
		}
		if status.Status == "denied" {
			return answer, webUtilityIPFailureError{"rateLimited"}
		}
		if status.Status != "ok" && status.Status != "warning" {
			return answer, webUtilityIPFailureError{"failed"}
		}
		if err := json.Unmarshal(data, &entries); err != nil {
			return answer, err
		}
		var v struct {
			Network struct {
				Type         string          `json:"type"`
				Organisation string          `json:"organisation"`
				ASN          webUtilityIPASN `json:"asn"`
			} `json:"network"`
			Detections struct{ Hosting, Proxy, VPN, Tor bool } `json:"detections"`
		}
		if raw, ok := entries[ip]; !ok || json.Unmarshal(raw, &v) != nil {
			return answer, webUtilityIPFailureError{"noType"}
		}
		declared := ""
		switch v.Network.Type {
		case "Residential":
			declared = "residential"
		case "Business":
			declared = "business"
		}
		q.Type = webUtilityIPPick(v.Detections.Hosting || v.Network.Type == "Hosting", v.Network.Type == "Wireless", declared)
		q.Organization, q.ASN = webUtilityIPText(v.Network.Organisation, ""), v.Network.ASN.value
		q.IsProxy, q.IsVPN, q.IsTor = v.Detections.Proxy, v.Detections.VPN, v.Detections.Tor
	case "ipApiIs":
		var v struct {
			IP      string                      `json:"ip"`
			Company struct{ Type, Name string } `json:"company"`
			ASN     struct {
				Type string          `json:"type"`
				Org  string          `json:"org"`
				ASN  webUtilityIPASN `json:"asn"`
			} `json:"asn"`
			Datacenter bool `json:"is_datacenter"`
			Mobile     bool `json:"is_mobile"`
			Proxy      bool `json:"is_proxy"`
			VPN        bool `json:"is_vpn"`
			Tor        bool `json:"is_tor"`
			Abuser     bool `json:"is_abuser"`
		}
		if err := decode(&v); err != nil {
			return answer, err
		}
		if err := webUtilityIPMatch(ip, v.IP); err != nil {
			return answer, err
		}
		declared := webUtilityIPType(v.Company.Type)
		if declared == "" {
			declared = webUtilityIPType(v.ASN.Type)
		}
		q.Type, q.Organization, q.ASN = webUtilityIPPick(v.Datacenter, v.Mobile, declared), webUtilityIPText(v.Company.Name, v.ASN.Org), v.ASN.ASN.value
		q.IsProxy, q.IsVPN, q.IsTor, q.IsAbuser = v.Proxy, v.VPN, v.Tor, v.Abuser
	default:
		return answer, webUtilityIPFailureError{"failed"}
	}
	if q.Type == "" {
		return answer, webUtilityIPFailureError{"noType"}
	}
	if err := webAPIValidateIPQuality(*q); err != nil {
		return answer, webUtilityIPFailureError{"failed"}
	}
	return answer, nil
}
