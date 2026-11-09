package service

import (
	"errors"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/util/password"

	"gorm.io/gorm"
)

type UserService struct {
}

func (s *UserService) GetFirstUser() (*model.User, error) {
	db := database.GetDB()

	user := &model.User{}
	err := db.Model(model.User{}).
		First(user).
		Error
	if err != nil {
		return nil, err
	}
	return user, nil
}

// CheckUser validates the credentials and returns the user on success.
//
// Passwords are stored as bcrypt hashes. Databases created by old x-ui versions
// contain plain-text passwords; those still work and are transparently upgraded
// to a bcrypt hash on the first successful login.
func (s *UserService) CheckUser(username string, plainPassword string) *model.User {
	if username == "" || plainPassword == "" {
		return nil
	}
	db := database.GetDB()

	user := &model.User{}
	err := db.Model(model.User{}).
		Where("username = ?", username).
		First(user).
		Error
	if err == gorm.ErrRecordNotFound {
		// burn the same amount of CPU as a real check to avoid a timing oracle
		// that would reveal which usernames exist
		password.DummyCheck(plainPassword)
		return nil
	} else if err != nil {
		logger.Warning("check user err:", err)
		password.DummyCheck(plainPassword)
		return nil
	}

	ok, legacy := password.Verify(user.Password, plainPassword)
	if !ok {
		return nil
	}
	if legacy {
		// upgrade the stored plain-text password in place
		hashed, err := password.Hash(plainPassword)
		if err != nil {
			logger.Warning("upgrade legacy password hash failed:", err)
		} else if err := db.Model(model.User{}).
			Where("id = ?", user.Id).
			Update("password", hashed).
			Error; err != nil {
			logger.Warning("upgrade legacy password hash failed:", err)
		} else {
			logger.Infof("user %d: legacy plain-text password upgraded to bcrypt", user.Id)
			user.Password = hashed
		}
	}
	return user
}

// UpdateUser stores a new username/password pair for the given user id.
func (s *UserService) UpdateUser(id int, username string, plainPassword string) error {
	if username == "" || plainPassword == "" {
		return errors.New("username and password can not be empty")
	}
	hashed, err := password.Hash(plainPassword)
	if err != nil {
		return err
	}
	db := database.GetDB()
	return db.Model(model.User{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"username": username,
			"password": hashed,
		}).
		Error
}

// UpdateFirstUser updates the first (and normally only) user of the panel.
func (s *UserService) UpdateFirstUser(username string, plainPassword string) error {
	if username == "" {
		return errors.New("username can not be empty")
	} else if plainPassword == "" {
		return errors.New("password can not be empty")
	}
	hashed, err := password.Hash(plainPassword)
	if err != nil {
		return err
	}
	db := database.GetDB()
	user := &model.User{}
	err = db.Model(model.User{}).First(user).Error
	if database.IsNotFound(err) {
		user.Username = username
		user.Password = hashed
		return db.Model(model.User{}).Create(user).Error
	} else if err != nil {
		return err
	}
	user.Username = username
	user.Password = hashed
	return db.Save(user).Error
}
