package service

import (
	"encoding/json"
	"fmt"
	"time"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/util/common"
	"x-ui/xray"

	"gorm.io/gorm"
)

type InboundService struct {
}

// ResetTraffic zeroes the up/down counters of an inbound.
func (s *InboundService) ResetTraffic(id int) error {
	db := database.GetDB()
	return db.Model(model.Inbound{}).Where("id = ?", id).
		Updates(map[string]interface{}{"up": 0, "down": 0}).Error
}

func (s *InboundService) GetInbounds(userId int) ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Where("user_id = ?", userId).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func (s *InboundService) GetAllInbounds() ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

// NextTag returns a unique tag for an inbound on the given port. TCP and UDP
// may legitimately share a port number, so a plain "inbound-<port>" is not
// unique enough.
func (s *InboundService) NextTag(port int) string {
	db := database.GetDB()
	base := fmt.Sprintf("inbound-%d", port)
	tag := base
	for i := 2; i < 100; i++ {
		var count int64
		_ = db.Model(model.Inbound{}).Where("tag = ?", tag).Count(&count).Error
		if count == 0 {
			return tag
		}
		tag = fmt.Sprintf("%s-%d", base, i)
	}
	return fmt.Sprintf("%s-%d", base, time.Now().Unix())
}

// isUdpInbound reports whether an inbound occupies an UDP port.
func isUdpInbound(protocol string, streamSettings string) bool {
	if protocol == "hysteria" || protocol == "hysteria2" {
		return true
	}
	stream := struct {
		Network string `json:"network"`
	}{}
	_ = json.Unmarshal([]byte(streamSettings), &stream)
	switch stream.Network {
	case "kcp", "quic", "hysteria":
		return true
	}
	return false
}

// checkPortExist reports a conflict only when the same port is already used by
// an inbound of the same socket type (TCP vs UDP), so that e.g. TCP 3389 and
// UDP 3389 can coexist.
func (s *InboundService) checkPortExist(port int, protocol string, streamSettings string, ignoreId int) (bool, error) {
	db := database.GetDB()
	inbounds := make([]*model.Inbound, 0)
	query := db.Model(model.Inbound{}).Where("port = ?", port)
	if ignoreId > 0 {
		query = query.Where("id != ?", ignoreId)
	}
	if err := query.Find(&inbounds).Error; err != nil {
		return false, err
	}
	udp := isUdpInbound(protocol, streamSettings)
	for _, inbound := range inbounds {
		if isUdpInbound(string(inbound.Protocol), inbound.StreamSettings) == udp {
			return true, nil
		}
	}
	return false, nil
}

func (s *InboundService) AddInbound(inbound *model.Inbound) error {
	exist, err := s.checkPortExist(inbound.Port, string(inbound.Protocol), inbound.StreamSettings, 0)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("端口已存在:", inbound.Port)
	}
	db := database.GetDB()
	return db.Save(inbound).Error
}

func (s *InboundService) AddInbounds(inbounds []*model.Inbound) error {
	for _, inbound := range inbounds {
		exist, err := s.checkPortExist(inbound.Port, string(inbound.Protocol), inbound.StreamSettings, 0)
		if err != nil {
			return err
		}
		if exist {
			return common.NewError("端口已存在:", inbound.Port)
		}
	}

	db := database.GetDB()
	tx := db.Begin()
	var err error
	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	for _, inbound := range inbounds {
		err = tx.Save(inbound).Error
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *InboundService) DelInbound(id int) error {
	db := database.GetDB()
	return db.Delete(model.Inbound{}, id).Error
}

func (s *InboundService) GetInbound(id int) (*model.Inbound, error) {
	db := database.GetDB()
	inbound := &model.Inbound{}
	err := db.Model(model.Inbound{}).First(inbound, id).Error
	if err != nil {
		return nil, err
	}
	return inbound, nil
}

func (s *InboundService) UpdateInbound(inbound *model.Inbound) error {
	exist, err := s.checkPortExist(inbound.Port, string(inbound.Protocol), inbound.StreamSettings, inbound.Id)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("端口已存在:", inbound.Port)
	}

	oldInbound, err := s.GetInbound(inbound.Id)
	if err != nil {
		return err
	}
	oldInbound.Up = inbound.Up
	oldInbound.Down = inbound.Down
	oldInbound.Total = inbound.Total
	oldInbound.Remark = inbound.Remark
	oldInbound.Enable = inbound.Enable
	oldInbound.ExpiryTime = inbound.ExpiryTime
	oldInbound.Listen = inbound.Listen
	oldInbound.Port = inbound.Port
	oldInbound.Protocol = inbound.Protocol
	oldInbound.Settings = inbound.Settings
	oldInbound.StreamSettings = inbound.StreamSettings
	oldInbound.Sniffing = inbound.Sniffing
	oldInbound.Tag = fmt.Sprintf("inbound-%v", inbound.Port)

	db := database.GetDB()
	return db.Save(oldInbound).Error
}

func (s *InboundService) AddTraffic(traffics []*xray.Traffic) (err error) {
	if len(traffics) == 0 {
		return nil
	}
	db := database.GetDB()
	db = db.Model(model.Inbound{})
	tx := db.Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()
	for _, traffic := range traffics {
		if traffic.IsInbound {
			err = tx.Where("tag = ?", traffic.Tag).
				UpdateColumn("up", gorm.Expr("up + ?", traffic.Up)).
				UpdateColumn("down", gorm.Expr("down + ?", traffic.Down)).
				Error
			if err != nil {
				return
			}
		}
	}
	return
}

func (s *InboundService) DisableInvalidInbounds() (int64, error) {
	db := database.GetDB()
	now := time.Now().Unix() * 1000
	result := db.Model(model.Inbound{}).
		Where("((total > 0 and up + down >= total) or (expiry_time > 0 and expiry_time <= ?)) and enable = ?", now, true).
		Update("enable", false)
	err := result.Error
	count := result.RowsAffected
	return count, err
}
