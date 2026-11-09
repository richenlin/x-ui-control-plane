package service

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"x-ui/database"
	"x-ui/database/model"
)

// InboundLink is the panel-friendly view of an inbound plus its client share
// link. It powers the dashboard "inbounds" card and the copy-link action.
type InboundLink struct {
	Id       int      `json:"id"`
	Remark   string   `json:"remark"`
	Protocol string   `json:"protocol"`
	Port     int      `json:"port"`
	Enable   bool     `json:"enable"`
	Network  string   `json:"network"`
	Security string   `json:"security"`
	Features []string `json:"features"`
	Up       int64    `json:"up"`
	Down     int64    `json:"down"`
	Total    int64    `json:"total"`
	Link     string   `json:"link"`
	Error    string   `json:"error,omitempty"`
}

type inboundUser struct {
	Id       string `json:"id"`
	AlterId  int    `json:"alterId"`
	Email    string `json:"email"`
	Flow     string `json:"flow"`
	Password string `json:"password"`
	Auth     string `json:"auth"`
	Method   string `json:"method"`
}

type inboundSettings struct {
	Clients    []inboundUser `json:"clients"`
	Decryption string        `json:"decryption"`
	Method     string        `json:"method"`
	Password   string        `json:"password"`
	Version    int           `json:"version"`
}

type streamSettings struct {
	Network         string `json:"network"`
	Security        string `json:"security"`
	TLSSettings     *struct {
		ServerName string   `json:"serverName"`
		ALPN       []string `json:"alpn"`
	} `json:"tlsSettings"`
	RealitySettings *struct {
		ServerNames []string `json:"serverNames"`
		PrivateKey  string   `json:"privateKey"`
		ShortIds    []string `json:"shortIds"`
	} `json:"realitySettings"`
	WSSettings *struct {
		Path string `json:"path"`
		Host string `json:"host"`
	} `json:"wsSettings"`
	GRPCSettings *struct {
		ServiceName string `json:"serviceName"`
	} `json:"grpcSettings"`
	KCPSettings *struct {
		Header struct {
			Type string `json:"type"`
		} `json:"header"`
		Seed string `json:"seed"`
	} `json:"kcpSettings"`
	HTTPSettings *struct {
		Host []string `json:"host"`
		Path string   `json:"path"`
	} `json:"httpSettings"`
}

func (s *InboundService) GetInboundLinks(host string) ([]*InboundLink, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	if err := db.Model(model.Inbound{}).Order("id").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	links := make([]*InboundLink, 0, len(inbounds))
	for _, inbound := range inbounds {
		links = append(links, buildInboundLink(inbound, host))
	}
	return links, nil
}

func (s *InboundService) GetInboundLink(host string, id int) (*InboundLink, error) {
	db := database.GetDB()
	inbound := &model.Inbound{}
	if err := db.Model(model.Inbound{}).Where("id = ?", id).First(inbound).Error; err != nil {
		return nil, err
	}
	return buildInboundLink(inbound, host), nil
}

func featureList(ss *streamSettings, settings *inboundSettings) []string {
	features := make([]string, 0, 4)
	if ss.Network != "" {
		features = append(features, ss.Network)
	}
	switch {
	case ss.Security == "reality":
		features = append(features, "reality")
	case ss.Security == "tls":
		features = append(features, "tls")
	}
	if len(settings.Clients) > 0 && strings.Contains(settings.Clients[0].Flow, "vision") {
		features = append(features, "vision")
	}
	if settings.Decryption != "" && settings.Decryption != "none" {
		features = append(features, "vless-encryption")
	}
	return features
}

func buildInboundLink(inbound *model.Inbound, host string) *InboundLink {
	link := &InboundLink{
		Id:       inbound.Id,
		Remark:   inbound.Remark,
		Protocol: string(inbound.Protocol),
		Port:     inbound.Port,
		Enable:   inbound.Enable,
		Up:       inbound.Up,
		Down:     inbound.Down,
		Total:    inbound.Total,
	}

	settings := &inboundSettings{}
	if inbound.Settings != "" {
		if err := json.Unmarshal([]byte(inbound.Settings), settings); err != nil {
			link.Error = "settings 解析失败"
			return link
		}
	}
	ss := &streamSettings{}
	if inbound.StreamSettings != "" {
		_ = json.Unmarshal([]byte(inbound.StreamSettings), ss)
	}
	link.Network = ss.Network
	link.Security = ss.Security
	link.Features = featureList(ss, settings)

	if inbound.Listen != "" && inbound.Listen != "0.0.0.0" && inbound.Listen != "::" {
		host = inbound.Listen
	}
	if host == "" {
		host = "127.0.0.1"
	}
	address := fmt.Sprintf("%s:%d", host, inbound.Port)

	switch string(inbound.Protocol) {
	case "vless":
		link.Link, link.Error = buildVlessLink(inbound, settings, ss, address)
	case "vmess":
		link.Link, link.Error = buildVmessLink(settings, ss, address, inbound.Remark)
	case "trojan":
		if len(settings.Clients) == 0 {
			link.Error = "缺少客户端配置"
			break
		}
		q := url.Values{}
		q.Set("security", orDefault(ss.Security, "tls"))
		if ss.TLSSettings != nil && ss.TLSSettings.ServerName != "" {
			q.Set("sni", ss.TLSSettings.ServerName)
		}
		link.Link = fmt.Sprintf("trojan://%s@%s?%s#%s",
			url.QueryEscape(settings.Clients[0].Password), address, q.Encode(),
			url.QueryEscape(inbound.Remark))
	case "shadowsocks":
		method, password := settings.Method, settings.Password
		if len(settings.Clients) > 0 && settings.Clients[0].Method != "" {
			method, password = settings.Clients[0].Method, settings.Clients[0].Password
		}
		if method == "" || password == "" {
			link.Error = "缺少 method/password"
			break
		}
		userInfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
		link.Link = fmt.Sprintf("ss://%s@%s#%s", userInfo, address, url.QueryEscape(inbound.Remark))
	case "hysteria", "hysteria2":
		auth := ""
		if len(settings.Clients) > 0 {
			auth = settings.Clients[0].Auth
		}
		if auth == "" {
			link.Error = "缺少 hysteria2 认证信息"
			break
		}
		q := url.Values{}
		if ss.TLSSettings != nil && ss.TLSSettings.ServerName != "" {
			q.Set("sni", ss.TLSSettings.ServerName)
		}
		q.Set("insecure", "0")
		link.Link = fmt.Sprintf("hysteria2://%s@%s/?%s#%s", auth, address, q.Encode(),
			url.QueryEscape(inbound.Remark))
	default:
		link.Error = "该协议不支持生成分享链接"
	}
	return link
}

func buildVlessLink(inbound *model.Inbound, settings *inboundSettings, ss *streamSettings, address string) (string, string) {
	if len(settings.Clients) == 0 || settings.Clients[0].Id == "" {
		return "", "缺少客户端 UUID"
	}
	user := settings.Clients[0]
	q := url.Values{}
	q.Set("type", orDefault(ss.Network, "tcp"))
	q.Set("encryption", "none")

	switch ss.Security {
	case "reality":
		if ss.RealitySettings == nil || ss.RealitySettings.PrivateKey == "" {
			return "", "REALITY 缺少 privateKey"
		}
		pub, err := x25519PublicKey(ss.RealitySettings.PrivateKey)
		if err != nil {
			return "", "REALITY 公钥推导失败"
		}
		q.Set("security", "reality")
		q.Set("pbk", pub)
		if len(ss.RealitySettings.ShortIds) > 0 {
			q.Set("sid", ss.RealitySettings.ShortIds[0])
		}
		if len(ss.RealitySettings.ServerNames) > 0 {
			q.Set("sni", ss.RealitySettings.ServerNames[0])
		}
		q.Set("fp", "chrome")
	case "tls":
		q.Set("security", "tls")
		if ss.TLSSettings != nil {
			if ss.TLSSettings.ServerName != "" {
				q.Set("sni", ss.TLSSettings.ServerName)
			}
			if len(ss.TLSSettings.ALPN) > 0 {
				q.Set("alpn", strings.Join(ss.TLSSettings.ALPN, ","))
			}
		}
		q.Set("fp", "chrome")
	default:
		q.Set("security", "none")
	}

	if settings.Decryption != "" && settings.Decryption != "none" {
		enc := vlessEncryptionFor(string(inbound.Protocol))
		if enc == "" {
			return "", "启用了 VLESS Encryption，但面板缺少客户端 encryption 串"
		}
		q.Set("encryption", enc)
	}

	if user.Flow != "" {
		q.Set("flow", user.Flow)
	}
	switch ss.Network {
	case "ws":
		if ss.WSSettings != nil {
			q.Set("path", ss.WSSettings.Path)
			if ss.WSSettings.Host != "" {
				q.Set("host", ss.WSSettings.Host)
			}
		}
	case "grpc":
		if ss.GRPCSettings != nil {
			q.Set("serviceName", ss.GRPCSettings.ServiceName)
		}
	case "h2", "http":
		if ss.HTTPSettings != nil {
			q.Set("path", ss.HTTPSettings.Path)
			if len(ss.HTTPSettings.Host) > 0 {
				q.Set("host", ss.HTTPSettings.Host[0])
			}
		}
	}

	return fmt.Sprintf("vless://%s@%s?%s#%s", user.Id, address, q.Encode(),
		url.QueryEscape(inbound.Remark)), ""
}

func buildVmessLink(settings *inboundSettings, ss *streamSettings, address string, remark string) (string, string) {
	if len(settings.Clients) == 0 || settings.Clients[0].Id == "" {
		return "", "缺少客户端 UUID"
	}
	user := settings.Clients[0]
	host, port, ok := strings.Cut(address, ":")
	if !ok {
		return "", "地址格式错误"
	}
	cfg := map[string]string{
		"v":    "2",
		"ps":   remark,
		"add":  host,
		"port": port,
		"id":   user.Id,
		"aid":  fmt.Sprintf("%d", user.AlterId),
		"scy":  "auto",
		"net":  orDefault(ss.Network, "tcp"),
		"type": "none",
		"host": "",
		"path": "",
		"tls":  "",
		"sni":  "",
	}
	if ss.Security == "tls" {
		cfg["tls"] = "tls"
		if ss.TLSSettings != nil && ss.TLSSettings.ServerName != "" {
			cfg["sni"] = ss.TLSSettings.ServerName
		}
	}
	switch ss.Network {
	case "ws":
		if ss.WSSettings != nil {
			cfg["path"] = ss.WSSettings.Path
			cfg["host"] = ss.WSSettings.Host
		}
	case "grpc":
		cfg["type"] = "gun"
		if ss.GRPCSettings != nil {
			cfg["path"] = ss.GRPCSettings.ServiceName
		}
	case "kcp":
		if ss.KCPSettings != nil {
			cfg["type"] = orDefault(ss.KCPSettings.Header.Type, "none")
			cfg["seed"] = ss.KCPSettings.Seed
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err.Error()
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(raw), ""
}

// vlessEncryptionFor returns the client side encryption string that pairs with
// the decryption string stored in the inbound settings.
func vlessEncryptionFor(protocol string) string {
	db := database.GetDB()
	if db == nil {
		return ""
	}
	setting := &model.Setting{}
	err := db.Model(model.Setting{}).Where("key = ?", "vlessEncryption").First(setting).Error
	if err != nil {
		return ""
	}
	return setting.Value
}

// x25519PublicKey derives the REALITY public key from the stored private key.
func x25519PublicKey(privateKey string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(privateKey)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("invalid x25519 private key")
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
