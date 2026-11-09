package controller

import (
	"github.com/gin-gonic/gin"
	"net"
	"net/http"
	"strings"
	"x-ui/config"
	"x-ui/logger"
	"x-ui/web/entity"
)

func getUriId(c *gin.Context) int64 {
	s := struct {
		Id int64 `uri:"id"`
	}{}

	_ = c.BindUri(&s)
	return s.Id
}

// getRemoteIp returns the address of the direct peer. Forwarding headers are
// only honoured when XUI_TRUST_PROXY=true (i.e. the panel really sits behind a
// trusted reverse proxy), otherwise an attacker could spoof X-Forwarded-For and
// bypass per-IP rate limiting.
func getRemoteIp(c *gin.Context) string {
	if config.TrustProxy() {
		if value := c.GetHeader("X-Real-IP"); value != "" {
			return strings.TrimSpace(value)
		}
		if value := c.GetHeader("X-Forwarded-For"); value != "" {
			ips := strings.Split(value, ",")
			if ip := strings.TrimSpace(ips[0]); ip != "" {
				return ip
			}
		}
	}
	addr := c.Request.RemoteAddr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func jsonMsg(c *gin.Context, msg string, err error) {
	jsonMsgObj(c, msg, nil, err)
}

func jsonObj(c *gin.Context, obj interface{}, err error) {
	jsonMsgObj(c, "", obj, err)
}

func jsonMsgObj(c *gin.Context, msg string, obj interface{}, err error) {
	m := entity.Msg{
		Obj: obj,
	}
	if err == nil {
		m.Success = true
		if msg != "" {
			m.Msg = msg + "成功"
		}
	} else {
		m.Success = false
		m.Msg = msg + "失败: " + err.Error()
		logger.Warning(msg+"失败: ", err)
	}
	c.JSON(http.StatusOK, m)
}

func pureJsonMsg(c *gin.Context, success bool, msg string) {
	if success {
		c.JSON(http.StatusOK, entity.Msg{
			Success: true,
			Msg:     msg,
		})
	} else {
		c.JSON(http.StatusOK, entity.Msg{
			Success: false,
			Msg:     msg,
		})
	}
}

func html(c *gin.Context, name string, title string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["title"] = title
	data["request_uri"] = c.Request.RequestURI
	data["base_path"] = c.GetString("base_path")
	data["cur_ver"] = curVerOf(c)
	c.HTML(http.StatusOK, name, getContext(data))
}

// curVerOf returns the build fingerprint of the embedded assets, injected by
// the web layer middleware, so asset URLs change whenever the front-end does.
func curVerOf(c *gin.Context) string {
	if v := c.GetString("cur_ver"); v != "" {
		return v
	}
	return config.GetVersion()
}

func getContext(h gin.H) gin.H {
	a := gin.H{
		"cur_ver": config.GetVersion(),
	}
	if h != nil {
		for key, value := range h {
			a[key] = value
		}
	}
	return a
}

func isAjax(c *gin.Context) bool {
	return c.GetHeader("X-Requested-With") == "XMLHttpRequest"
}
