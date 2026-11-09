package controller

import (
	"net/http"
	"strconv"
	"time"

	"x-ui/logger"
	"x-ui/web/entity"
	"x-ui/web/guard"
	"x-ui/web/job"
	"x-ui/web/service"
	"x-ui/web/session"

	"github.com/gin-gonic/gin"
)

type LoginForm struct {
	Username string `json:"username" form:"username"`
	Password string `json:"password" form:"password"`
}

type IndexController struct {
	BaseController

	userService service.UserService
}

func NewIndexController(g *gin.RouterGroup) *IndexController {
	a := &IndexController{}
	a.initRouter(g)
	return a
}

func (a *IndexController) initRouter(g *gin.RouterGroup) {
	g.GET("/", a.index)
	g.POST("/login", a.login)
	g.GET("/logout", a.logout)
}

func (a *IndexController) index(c *gin.Context) {
	if session.IsLogin(c) {
		c.Redirect(http.StatusTemporaryRedirect, "xui/")
		return
	}
	html(c, "login.html", "登录", nil)
}

// loginTooManyRequests answers a login attempt that was rejected by the guard.
func loginTooManyRequests(c *gin.Context, d guard.Decision) {
	retry := int(d.RetryAfter.Seconds() + 0.5)
	if retry < 1 {
		retry = 1
	}
	c.Header("Retry-After", strconv.Itoa(retry))
	msg := d.Message + "（约 " + guard.FormatDuration(d.RetryAfter) + " 后恢复）"
	c.JSON(http.StatusTooManyRequests, entity.Msg{Success: false, Msg: msg})
}

func (a *IndexController) login(c *gin.Context) {
	ip := getRemoteIp(c)
	guardInstance := getLoginGuard()

	if d := guardInstance.Check(ip); d.Blocked {
		logger.Warningf("login blocked by guard, ip: %s, reason: %s", ip, d.Message)
		loginTooManyRequests(c, d)
		return
	}

	var form LoginForm
	err := c.ShouldBind(&form)
	if err != nil {
		pureJsonMsg(c, false, "数据格式错误")
		return
	}
	if form.Username == "" {
		pureJsonMsg(c, false, "请输入用户名")
		return
	}
	if form.Password == "" {
		pureJsonMsg(c, false, "请输入密码")
		return
	}
	user := a.userService.CheckUser(form.Username, form.Password)
	timeStr := time.Now().Format("2006-01-02 15:04:05")
	if user == nil {
		d := guardInstance.Failure(ip)
		// never log the attempted password; only the username and the source IP
		logger.Warningf("login failed, username: %q, ip: %s", form.Username, ip)
		if d.Blocked {
			job.NewStatsNotifyJob().UserLoginNotify(form.Username, ip, timeStr, job.LoginFail)
			loginTooManyRequests(c, d)
			return
		}
		job.NewStatsNotifyJob().UserLoginNotify(form.Username, ip, timeStr, job.LoginFail)
		pureJsonMsg(c, false, "用户名或密码错误")
		return
	}

	guardInstance.Success(ip)
	logger.Infof("login success, user: %q, ip: %s", form.Username, ip)
	job.NewStatsNotifyJob().UserLoginNotify(form.Username, ip, timeStr, job.LoginSuccess)

	err = session.SetLoginUser(c, user)
	if err != nil {
		logger.Warning("save session failed:", err)
		jsonMsg(c, "登录", err)
		return
	}
	logger.Info("user", user.Id, "login success")
	jsonMsg(c, "登录", nil)
}

func (a *IndexController) logout(c *gin.Context) {
	user := session.GetLoginUser(c)
	if user != nil {
		logger.Info("user", user.Id, "logout")
	}
	session.ClearSession(c)
	c.Redirect(http.StatusTemporaryRedirect, c.GetString("base_path"))
}
