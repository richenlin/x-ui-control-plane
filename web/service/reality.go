package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/xray"
)

// RealityService manages REALITY key material: it powers both the inbound form
// and the key rotation panel, so the deployment no longer depends on the
// provisioning script once installed.
type RealityService struct {
	xrayService XrayService
}

type RealityKeyPair struct {
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
	ShortId    string `json:"shortId"`
}

// RealityInbound is the panel view of one REALITY-carrying inbound.
type RealityInbound struct {
	Id          int      `json:"id"`
	Remark      string   `json:"remark"`
	Port        int      `json:"port"`
	Protocol    string   `json:"protocol"`
	Enabled     bool     `json:"enabled"`
	Dest        string   `json:"dest"`
	ServerNames []string `json:"serverNames"`
	PublicKey   string   `json:"publicKey"`
	ShortIds    []string `json:"shortIds"`
}

type realitySettings struct {
	Show        bool     `json:"show"`
	Dest        string   `json:"dest"`
	Xver        int      `json:"xver"`
	ServerNames []string `json:"serverNames"`
	PrivateKey  string   `json:"privateKey"`
	ShortIds    []string `json:"shortIds"`
}

func (s *RealityService) generate() (*RealityKeyPair, error) {
	out, err := exec.Command(xray.GetBinaryPath(), "x25519").Output()
	if err != nil {
		return nil, err
	}
	priv := regexp.MustCompile(`PrivateKey:\s*(\S+)`).FindStringSubmatch(string(out))
	pub := regexp.MustCompile(`(?:Password|PublicKey)(?: \(PublicKey\))?:\s*(\S+)`).FindStringSubmatch(string(out))
	if len(priv) < 2 || len(pub) < 2 {
		return nil, errors.New("cannot parse x25519 output")
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return &RealityKeyPair{
		PrivateKey: priv[1],
		PublicKey:  pub[1],
		ShortId:    hex.EncodeToString(buf),
	}, nil
}

// Generate creates a key pair for the inbound form.
func (s *RealityService) Generate() (*RealityKeyPair, error) {
	return s.generate()
}

// PublicKey derives the REALITY public key from a stored private key.
func (s *RealityService) PublicKey(privateKey string) (string, error) {
	return x25519PublicKey(privateKey)
}

// Status lists every inbound that uses REALITY together with its public key.
func (s *RealityService) Status() ([]*RealityInbound, error) {
	db := database.GetDB()
	inbounds := make([]*model.Inbound, 0)
	if err := db.Model(model.Inbound{}).Where("protocol IN ?", []string{"vless", "trojan"}).
		Order("id").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	result := make([]*RealityInbound, 0, len(inbounds))
	for _, inbound := range inbounds {
		stream, reality, err := parseReality(inbound)
		if err != nil || reality == nil || stream.Security != "reality" {
			continue
		}
		pub, _ := x25519PublicKey(reality.PrivateKey)
		result = append(result, &RealityInbound{
			Id:          inbound.Id,
			Remark:      inbound.Remark,
			Port:        inbound.Port,
			Protocol:    string(inbound.Protocol),
			Enabled:     inbound.Enable,
			Dest:        reality.Dest,
			ServerNames: reality.ServerNames,
			PublicKey:   pub,
			ShortIds:    reality.ShortIds,
		})
	}
	return result, nil
}

// Rotate regenerates the X25519 key pair (and shortIds) of one inbound, or of
// every REALITY inbound when id <= 0. Client links become invalid, so the
// operator must re-copy them afterwards.
func (s *RealityService) Rotate(id int) ([]*RealityInbound, error) {
	db := database.GetDB()
	inbounds := make([]*model.Inbound, 0)
	query := db.Model(model.Inbound{}).Where("protocol IN ?", []string{"vless", "trojan"})
	if id > 0 {
		query = query.Where("id = ?", id)
	}
	if err := query.Find(&inbounds).Error; err != nil {
		return nil, err
	}
	rotated := 0
	for _, inbound := range inbounds {
		stream, reality, err := parseReality(inbound)
		if err != nil || reality == nil || stream.Security != "reality" {
			continue
		}
		pair, err := s.generate()
		if err != nil {
			return nil, err
		}
		reality.PrivateKey = pair.PrivateKey
		count := len(reality.ShortIds)
		if count < 1 {
			count = 1
		}
		shortIds := make([]string, 0, count)
		for i := 0; i < count; i++ {
			sid, err := newShortId()
			if err != nil {
				return nil, err
			}
			shortIds = append(shortIds, sid)
		}
		reality.ShortIds = shortIds
		if err := saveReality(inbound, stream, reality); err != nil {
			return nil, err
		}
		rotated++
		logger.Infof("REALITY key pair rotated for inbound %d (%s)", inbound.Id, inbound.Remark)
	}
	if rotated == 0 && id > 0 {
		return nil, errors.New("该入站未启用 REALITY")
	}
	if rotated > 0 {
		if err := s.xrayService.RestartXray(false); err != nil {
			logger.Warning("restart xray after REALITY rotation failed:", err)
			s.xrayService.SetToNeedRestart()
		}
	}
	return s.Status()
}

func newShortId() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type parsedStream struct {
	Security      string          `json:"security"`
	RealityConfig json.RawMessage `json:"realitySettings"`
}

func parseReality(inbound *model.Inbound) (*parsedStream, *realitySettings, error) {
	stream := &parsedStream{}
	if inbound.StreamSettings == "" {
		return stream, nil, nil
	}
	if err := json.Unmarshal([]byte(inbound.StreamSettings), stream); err != nil {
		return nil, nil, err
	}
	if len(stream.RealityConfig) == 0 {
		return stream, nil, nil
	}
	reality := &realitySettings{}
	if err := json.Unmarshal(stream.RealityConfig, reality); err != nil {
		return nil, nil, err
	}
	return stream, reality, nil
}

func saveReality(inbound *model.Inbound, stream *parsedStream, reality *realitySettings) error {
	raw, err := json.Marshal(reality)
	if err != nil {
		return err
	}
	full := map[string]interface{}{}
	if inbound.StreamSettings != "" {
		if err := json.Unmarshal([]byte(inbound.StreamSettings), &full); err != nil {
			return err
		}
	}
	full["security"] = "reality"
	full["realitySettings"] = json.RawMessage(raw)
	streamRaw, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		return err
	}
	db := database.GetDB()
	return db.Model(model.Inbound{}).Where("id = ?", inbound.Id).
		Update("stream_settings", string(streamRaw)).Error
}
