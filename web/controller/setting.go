package controller

import (
	"errors"
	"github.com/gin-gonic/gin"
	"time"
	"x-ui/database/model"
	"x-ui/web/entity"
	"x-ui/web/service"
	"x-ui/web/session"
)

type updateUserForm struct {
	OldUsername string `json:"oldUsername" form:"oldUsername"`
	OldPassword string `json:"oldPassword" form:"oldPassword"`
	NewUsername string `json:"newUsername" form:"newUsername"`
	NewPassword string `json:"newPassword" form:"newPassword"`
}

type SettingController struct {
	settingService  service.SettingService
	userService     service.UserService
	panelService    service.PanelService
	vlessEncService service.VlessEncService
	realityService  service.RealityService
}

func NewSettingController(g *gin.RouterGroup) *SettingController {
	a := &SettingController{}
	a.initRouter(g)
	return a
}

func (a *SettingController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/setting")

	g.POST("/all", a.getAllSetting)
	g.POST("/update", a.updateSetting)
	g.POST("/updateUser", a.updateUser)
	g.POST("/restartPanel", a.restartPanel)

	g.POST("/vlessEnc", a.vlessEncStatus)
	g.POST("/vlessEnc/pair", a.vlessEncPair)
	g.POST("/realityKeyPair", a.realityKeyPair)
	g.POST("/reality", a.realityStatus)
	g.POST("/reality/rotate/:id", a.realityRotate)
	g.POST("/vlessEnc/regenerate", a.vlessEncRegenerate)
	g.POST("/vlessEnc/disable", a.vlessEncDisable)
}

func (a *SettingController) getAllSetting(c *gin.Context) {
	allSetting, err := a.settingService.GetAllSetting()
	if err != nil {
		jsonMsg(c, "获取设置", err)
		return
	}
	jsonObj(c, allSetting, nil)
}

func (a *SettingController) updateSetting(c *gin.Context) {
	allSetting := &entity.AllSetting{}
	err := c.ShouldBind(allSetting)
	if err != nil {
		jsonMsg(c, "修改设置", err)
		return
	}
	err = a.settingService.UpdateAllSetting(allSetting)
	jsonMsg(c, "修改设置", err)
}

func (a *SettingController) updateUser(c *gin.Context) {
	form := &updateUserForm{}
	err := c.ShouldBind(form)
	if err != nil {
		jsonMsg(c, "修改用户", err)
		return
	}
	user := session.GetLoginUser(c)
	if user == nil {
		jsonMsg(c, "修改用户", errors.New("登录时效已过，请重新登录"))
		return
	}
	if user.Username != form.OldUsername || a.userService.CheckUser(form.OldUsername, form.OldPassword) == nil {
		jsonMsg(c, "修改用户", errors.New("原用户名或原密码错误"))
		return
	}
	if form.NewUsername == "" || form.NewPassword == "" {
		jsonMsg(c, "修改用户", errors.New("新用户名和新密码不能为空"))
		return
	}
	err = a.userService.UpdateUser(user.Id, form.NewUsername, form.NewPassword)
	if err == nil {
		// re-issue the session so that its signature matches the new credentials
		if err := session.SetLoginUser(c, &model.User{Id: user.Id, Username: form.NewUsername}); err != nil {
			jsonMsg(c, "修改用户", err)
			return
		}
	}
	jsonMsg(c, "修改用户", err)
}

func (a *SettingController) vlessEncStatus(c *gin.Context) {
	status, err := a.vlessEncService.Status()
	jsonObj(c, status, err)
}

func (a *SettingController) realityStatus(c *gin.Context) {
	list, err := a.realityService.Status()
	jsonObj(c, list, err)
}

func (a *SettingController) realityRotate(c *gin.Context) {
	id := getUriId(c)
	list, err := a.realityService.Rotate(int(id))
	jsonMsgObj(c, "轮换 REALITY 密钥", list, err)
}

func (a *SettingController) realityKeyPair(c *gin.Context) {
	pair, err := a.realityService.Generate()
	jsonMsgObj(c, "生成 REALITY 密钥", pair, err)
}

func (a *SettingController) vlessEncPair(c *gin.Context) {
	status, err := a.vlessEncService.GeneratePair()
	jsonMsgObj(c, "生成 VLESS Encryption 密钥", status, err)
}

func (a *SettingController) vlessEncRegenerate(c *gin.Context) {
	status, err := a.vlessEncService.Regenerate()
	jsonMsgObj(c, "重新生成 VLESS Encryption 密钥", status, err)
}

func (a *SettingController) vlessEncDisable(c *gin.Context) {
	status, err := a.vlessEncService.Disable()
	jsonMsgObj(c, "关闭 VLESS Encryption", status, err)
}

func (a *SettingController) restartPanel(c *gin.Context) {
	err := a.panelService.RestartPanel(time.Second * 3)
	jsonMsg(c, "重启面板", err)
}
