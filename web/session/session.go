package session

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/gob"
	"encoding/hex"
	"net/http"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
)

const (
	loginUser = "LOGIN_USER"
	loginSig  = "LOGIN_SIG"
)

func init() {
	gob.Register(model.User{})
}

// signature binds a session to the current username/password hash. Changing the
// password therefore invalidates every cookie that was issued before the change.
func signature(username, passwordHash string) string {
	h := sha256.Sum256([]byte(username + "\x00" + passwordHash))
	return hex.EncodeToString(h[:])
}

func findUser(id int) *model.User {
	if id <= 0 {
		return nil
	}
	db := database.GetDB()
	if db == nil {
		return nil
	}
	user := &model.User{}
	err := db.Model(model.User{}).Where("id = ?", id).First(user).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			logger.Warning("load session user failed:", err)
		}
		return nil
	}
	return user
}

// SetLoginUser stores the session. Only the user id/name and a signature are kept
// in the (client side) cookie - never the password or its hash.
func SetLoginUser(c *gin.Context, user *model.User) error {
	if user == nil {
		return nil
	}
	dbUser := findUser(user.Id)
	if dbUser == nil {
		return gorm.ErrRecordNotFound
	}
	stored := model.User{Id: dbUser.Id, Username: dbUser.Username}

	s := sessions.Default(c)
	s.Set(loginUser, stored)
	s.Set(loginSig, signature(dbUser.Username, dbUser.Password))
	s.Options(sessions.Options{
		Path:     "/",
		MaxAge:   0, // session cookie
		HttpOnly: true,
		Secure:   c.GetBool("secure_cookie"),
		SameSite: http.SameSiteLaxMode,
	})
	return s.Save()
}

func GetLoginUser(c *gin.Context) *model.User {
	s := sessions.Default(c)
	obj := s.Get(loginUser)
	if obj == nil {
		return nil
	}
	user, ok := obj.(model.User)
	if !ok {
		return nil
	}
	return &user
}

// IsLogin reports whether the request carries a valid, still up-to-date session.
func IsLogin(c *gin.Context) bool {
	user := GetLoginUser(c)
	if user == nil {
		return false
	}
	sig, _ := sessions.Default(c).Get(loginSig).(string)
	dbUser := findUser(user.Id)
	if dbUser == nil {
		ClearSession(c)
		return false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(signature(dbUser.Username, dbUser.Password))) != 1 {
		ClearSession(c)
		return false
	}
	return true
}

func ClearSession(c *gin.Context) {
	s := sessions.Default(c)
	s.Clear()
	s.Options(sessions.Options{
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.GetBool("secure_cookie"),
		SameSite: http.SameSiteLaxMode,
	})
	s.Save()
}
