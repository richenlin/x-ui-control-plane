package service

import (
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"strings"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/xray"
)

// VlessEncStatus describes the VLESS Encryption (post-quantum) setup of the
// panel: the server side string lives in the inbound settings, the client side
// string is kept in the panel settings table so links can be generated.
type VlessEncStatus struct {
	Enabled    bool   `json:"enabled"`
	Algorithm  string `json:"algorithm"`
	Decryption string `json:"decryption"`
	Encryption string `json:"encryption"`
	InboundId  int    `json:"inboundId"`
	Remark     string `json:"remark"`
}

type VlessEncService struct {
	xrayService XrayService
}

const vlessEncSettingKey = "vlessEncryption"

func (s *VlessEncService) getSetting() string {
	db := database.GetDB()
	if db == nil {
		return ""
	}
	setting := &model.Setting{}
	if err := db.Model(model.Setting{}).Where("key = ?", vlessEncSettingKey).First(setting).Error; err != nil {
		return ""
	}
	return setting.Value
}

func (s *VlessEncService) setSetting(value string) error {
	db := database.GetDB()
	if err := db.Model(model.Setting{}).Where("key = ?", vlessEncSettingKey).Delete(&model.Setting{}).Error; err != nil {
		return err
	}
	if value == "" {
		return nil
	}
	return db.Model(model.Setting{}).Create(&model.Setting{Key: vlessEncSettingKey, Value: value}).Error
}

// targetInbound returns the VLESS inbound that carries VLESS Encryption.
func (s *VlessEncService) targetInbound() (*model.Inbound, error) {
	db := database.GetDB()
	inbound := &model.Inbound{}
	// prefer the REALITY inbound (the stealth channel), fall back to any vless
	// inbound that already has a decryption string
	err := db.Model(model.Inbound{}).Where("tag = ?", "inbound-reality").First(inbound).Error
	if err == nil {
		// the REALITY inbound is the designated carrier, even when currently off
		return inbound, nil
	}
	candidates := make([]*model.Inbound, 0)
	if err := db.Model(model.Inbound{}).Where("protocol = ?", "vless").Find(&candidates).Error; err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if s.inboundDecryption(candidate) != "" {
			return candidate, nil
		}
	}
	return nil, errors.New("没有启用 VLESS Encryption 的入站")
}

func (s *VlessEncService) inboundDecryption(inbound *model.Inbound) string {
	settings := struct {
		Decryption string `json:"decryption"`
	}{}
	if inbound.Settings != "" {
		_ = json.Unmarshal([]byte(inbound.Settings), &settings)
	}
	if settings.Decryption == "" || settings.Decryption == "none" {
		return ""
	}
	return settings.Decryption
}

func (s *VlessEncService) Status() (*VlessEncStatus, error) {
	status := &VlessEncStatus{Encryption: s.getSetting()}
	inbound, err := s.targetInbound()
	if err != nil {
		return status, nil
	}
	dec := s.inboundDecryption(inbound)
	status.Enabled = dec != ""
	status.Decryption = dec
	status.InboundId = inbound.Id
	status.Remark = inbound.Remark
	if dec != "" {
		status.Algorithm = strings.SplitN(dec, ".", 2)[0]
	}
	if status.Encryption != "" && status.Algorithm == "" {
		status.Algorithm = strings.SplitN(status.Encryption, ".", 2)[0]
	}
	return status, nil
}

// generatePair runs the bundled core to obtain a fresh ML-KEM-768 pair.
func (s *VlessEncService) generatePair() (decryption string, encryption string, err error) {
	out, err := exec.Command(xray.GetBinaryPath(), "vlessenc").Output()
	if err != nil {
		return "", "", err
	}
	// The command prints two authentication options; prefer the post-quantum one.
	parts := strings.SplitN(string(out), "Authentication: ML-KEM-768", 2)
	if len(parts) != 2 {
		parts = strings.Split(string(out), "Authentication: X25519")
		if len(parts) != 2 {
			return "", "", errors.New("无法解析 vlessenc 输出")
		}
	}
	block := parts[1]
	d := regexp.MustCompile(`"decryption":\s*"([^"]+)"`).FindStringSubmatch(block)
	e := regexp.MustCompile(`"encryption":\s*"([^"]+)"`).FindStringSubmatch(block)
	if len(d) < 2 || len(e) < 2 {
		return "", "", errors.New("无法解析 vlessenc 输出")
	}
	return d[1], e[1], nil
}

// GeneratePair creates a new pair and stores only the client side string, so
// that a not-yet-saved inbound can be created from the panel form.
func (s *VlessEncService) GeneratePair() (*VlessEncStatus, error) {
	dec, enc, err := s.generatePair()
	if err != nil {
		return nil, err
	}
	if err := s.setSetting(enc); err != nil {
		return nil, err
	}
	return &VlessEncStatus{
		Enabled:    true,
		Algorithm:  strings.SplitN(dec, ".", 2)[0],
		Decryption: dec,
		Encryption: enc,
	}, nil
}

// Regenerate rotates the post-quantum key pair. Existing client links become
// invalid, so the caller should warn the operator.
func (s *VlessEncService) Regenerate() (*VlessEncStatus, error) {
	inbound, err := s.targetInbound()
	if err != nil {
		return nil, err
	}
	dec, enc, err := s.generatePair()
	if err != nil {
		return nil, err
	}
	if err := s.applyDecryption(inbound, dec); err != nil {
		return nil, err
	}
	if err := s.setSetting(enc); err != nil {
		return nil, err
	}
	logger.Infof("VLESS Encryption key pair rotated for inbound %d (%s)", inbound.Id, inbound.Remark)
	s.restartXray()
	return s.Status()
}

// Disable turns VLESS Encryption off (clients fall back to encryption=none).
func (s *VlessEncService) Disable() (*VlessEncStatus, error) {
	inbound, err := s.targetInbound()
	if err != nil {
		// nothing enabled: just clear the stored client string
		if err := s.setSetting(""); err != nil {
			return nil, err
		}
		return s.Status()
	}
	if err := s.applyDecryption(inbound, "none"); err != nil {
		return nil, err
	}
	if err := s.setSetting(""); err != nil {
		return nil, err
	}
	logger.Infof("VLESS Encryption disabled for inbound %d (%s)", inbound.Id, inbound.Remark)
	s.restartXray()
	return s.Status()
}

func (s *VlessEncService) applyDecryption(inbound *model.Inbound, decryption string) error {
	settings := map[string]interface{}{}
	if inbound.Settings != "" {
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			return err
		}
	}
	settings["decryption"] = decryption
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	db := database.GetDB()
	return db.Model(model.Inbound{}).Where("id = ?", inbound.Id).
		Update("settings", string(raw)).Error
}

func (s *VlessEncService) restartXray() {
	if err := s.xrayService.RestartXray(false); err != nil {
		logger.Warning("restart xray after VLESS Encryption change failed:", err)
		s.xrayService.SetToNeedRestart()
	}
}
