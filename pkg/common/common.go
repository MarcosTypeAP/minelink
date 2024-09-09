package common

import (
	"errors"
	"net"
	"os"
	"path"
)

var appDirname = "minelink-ap"

var appConfigDir string

func GetConfigPath(name string) string {
	if len(appConfigDir) > 0 {
		return path.Join(appConfigDir, name)
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		panic("WTF bro, what OS are you on???")
	}

	appConfigDir = path.Join(configDir, appDirname)
	if err := os.Mkdir(appConfigDir, 0o700); err != nil && !os.IsExist(err) {
		panic(err)
	}

	return GetConfigPath(name)
}

var appCacheDir string

func GetCachePath(name string) string {
	if len(appCacheDir) > 0 {
		return path.Join(appCacheDir, name)
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		panic("WTF bro, what OS are you on???")
	}
	appCacheDir = path.Join(cacheDir, appDirname)
	if err := os.Mkdir(appCacheDir, 0o700); err != nil && !os.IsExist(err) {
		panic(err)
	}

	return GetCachePath(name)
}

var appTempDir string

func GetTempPath(name string) string {
	if len(appTempDir) > 0 {
		return path.Join(appTempDir, name)
	}

	appTempDir = path.Join(os.TempDir(), appDirname)
	if err := os.Mkdir(appTempDir, 0o700); err != nil && !os.IsExist(err) {
		panic(err)
	}
	return GetTempPath(name)
}

var privateIP net.IP

func GetPrivateIP() net.IP {
	if privateIP != nil {
		return privateIP
	}

	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	privateIP = localAddr.IP

	return GetPrivateIP()
}

func IsConnTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	operr, ok := err.(*net.OpError)
	if ok {
		return operr.Timeout()
	}
	return false
}
