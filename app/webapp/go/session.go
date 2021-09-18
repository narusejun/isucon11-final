package main

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

func encodeSession(userID, userName string, isAdmin bool) string {
	isAdminStr := "false"
	if isAdmin {
		isAdminStr = "true"
	}

	return hex.EncodeToString([]byte(userID + "," + userName + "," + isAdminStr))
}

func decodeSession(raw string) (userID, userName string, isAdmin bool) {
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return "-", "-", false
	}

	val := strings.Split(string(decoded), ",")
	if len(val) != 3 {
		return "-", "-", false
	}

	return val[0], val[1], val[2] == "true"
}

func setSession(c echo.Context, userID, userName string, isAdmin bool) {
	c.SetCookie(&http.Cookie{
		Name:  SessionName,
		Value: encodeSession(userID, userName, isAdmin),

		Path:   "/",
		MaxAge: 3600,
	})
}

func getSession(c echo.Context) (userID, userName string, isAdmin bool) {
	cookie, err := c.Cookie(SessionName)
	if err != nil {
		return "-", "-", false
	}

	return decodeSession(cookie.Value)
}

func removeSession(c echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:   SessionName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}
