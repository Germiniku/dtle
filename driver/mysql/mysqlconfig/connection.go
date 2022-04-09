/*
 * Copyright (C) 2016-2018. ActionTech.
 * Based on: github.com/hashicorp/nomad, github.com/github/gh-ost .
 * License: MPL version 2: https://www.mozilla.org/en-US/MPL/2.0 .
 */

package mysqlconfig

import (
	"fmt"
)

// ConnectionConfig is the minimal configuration required to connect to a MySQL server
type ConnectionConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Charset  string
}

func (c *ConnectionConfig) GetDBUriByDbName(databaseName string) string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%v&maxAllowedPacket=0", c.User, c.Password, c.Host, c.Port, databaseName, c.Charset)
}

func (c *ConnectionConfig) GetDBUri() string {
	if "" == c.Charset {
		c.Charset = "utf8mb4"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/?timeout=5s&tls=false&autocommit=true&charset=%v&multiStatements=true&maxAllowedPacket=0", c.User, c.Password, c.Host, c.Port, c.Charset)
}

func (c *ConnectionConfig) GetSingletonDBUri() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/?timeout=5s&tls=false&autocommit=false&charset=%v&multiStatements=true&maxAllowedPacket=0", c.User, c.Password, c.Host, c.Port, c.Charset)
}

func (c *ConnectionConfig) GetAddr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}
