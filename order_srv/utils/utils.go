package utils

import (
	"net"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func Paginate(page, pageSize int) func(db *gorm.DB) *gorm.DB {
	return func (db *gorm.DB) *gorm.DB {
	  if page <= 0 {
		page = 1
	  }
	  switch {
	  case pageSize > 100:
		pageSize = 100
	  case pageSize <= 0:
		pageSize = 10
	  }
  
	  offset := (page - 1) * pageSize
	  return db.Offset(offset).Limit(pageSize)
	}
  }
  func GetFreePort()(int,error){
	addr,err:=net.ResolveTCPAddr("tcp","localhost:0")
	if err!=nil{
		return 0, err
	}

	l,err:=net.ListenTCP("tcp",addr)
	if err!=nil{
		return 0,err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port,nil
}