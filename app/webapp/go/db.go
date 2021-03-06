package main

import (
	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

func GetDB(batch bool) (*sqlx.DB, error) {
	mysqlConfig := mysql.NewConfig()
	mysqlConfig.Net = "tcp"
	mysqlConfig.Addr = GetEnv("MYSQL_HOSTNAME", "127.0.0.1") + ":" + GetEnv("MYSQL_PORT", "3306")
	mysqlConfig.User = GetEnv("MYSQL_USER", "isucon")
	mysqlConfig.Passwd = GetEnv("MYSQL_PASS", "isucon")
	mysqlConfig.DBName = GetEnv("MYSQL_DATABASE", "isucholar")
	mysqlConfig.ParseTime = true
	mysqlConfig.MultiStatements = batch
	// 必要なら
	// mysqlConfig.InterpolateParams = true

	return sqlx.Open("mysql", mysqlConfig.FormatDSN())
}

func GetSubDB(batch bool) (*sqlx.DB, error) {
	mysqlConfig := mysql.NewConfig()
	mysqlConfig.Net = "tcp"
	mysqlConfig.Addr = GetEnv("MYSQL_HOSTNAME_SUB", "127.0.0.1") + ":" + GetEnv("MYSQL_PORT", "3306")
	mysqlConfig.User = GetEnv("MYSQL_USER", "isucon")
	mysqlConfig.Passwd = GetEnv("MYSQL_PASS", "isucon")
	mysqlConfig.DBName = GetEnv("MYSQL_DATABASE", "isucholar")
	mysqlConfig.ParseTime = true
	mysqlConfig.MultiStatements = batch
	// 必要なら
	// mysqlConfig.InterpolateParams = true

	return sqlx.Open("mysql", mysqlConfig.FormatDSN())
}
