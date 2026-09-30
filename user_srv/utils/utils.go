package utils

import (
	"net"

	"golang.org/x/crypto/bcrypt"
)

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
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
