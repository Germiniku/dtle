module github.com/actiontech/dtle

go 1.12

require (
	github.com/LK4D4/joincontext v0.0.0-20171026170139-1724345da6d5 // indirect
	github.com/NYTimes/gziphandler v1.1.1 // indirect
	github.com/Shopify/sarama v1.26.4
	github.com/actiontech/golang-live-coverage-report v0.0.0-20210902074032-43aa91afdc2c
	github.com/alecthomas/template v0.0.0-20190718012654-fb15b899a751
	github.com/araddon/dateparse v0.0.0-20190622164848-0fb0a474d195 // indirect
	github.com/araddon/gou v0.0.0-20190110011759-c797efecbb61 // indirect
	github.com/araddon/qlbridge v0.0.0-00010101000000-000000000000
	github.com/armon/go-metrics v0.3.4
	github.com/cznic/mathutil v0.0.0-20181122101859-297441e03548
	github.com/cznic/parser v0.0.0-20181122101858-d773202d5b1f // indirect
	github.com/cznic/strutil v0.0.0-20181122101858-275e90344537 // indirect
	github.com/cznic/y v0.0.0-20181122101901-b05e8c2e8d7b // indirect
	github.com/dgrijalva/jwt-go v3.2.0+incompatible
	github.com/docker/go-units v0.4.0 // indirect
	github.com/docker/libkv v0.2.1
	github.com/go-playground/universal-translator v0.17.0 // indirect
	github.com/go-playground/validator v9.31.0+incompatible
	github.com/go-sql-driver/mysql v1.5.0
	github.com/golang/snappy v0.0.1 // indirect
	github.com/gorhill/cronexpr v0.0.0-20180427100037-88b0669f7d75 // indirect
	github.com/hashicorp/go-hclog v0.14.1
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/hashicorp/hcl2 v0.0.0-20191002203319-fb75b3253c80 // indirect
	github.com/hashicorp/nomad v1.1.2
	github.com/hashicorp/nomad/api v0.0.0-20200529203653-c4416b26d3eb
	github.com/issuj/gofaster v0.0.0-20170702192727-b08f1666d622 // indirect
	github.com/julienschmidt/httprouter v1.2.0
	github.com/labstack/echo/v4 v4.2.1
	github.com/leodido/go-urn v1.2.1 // indirect
	github.com/lytics/datemath v0.0.0-20180727225141-3ada1c10b5de // indirect
	github.com/mb0/glob v0.0.0-20160210091149-1eb79d2de6c4 // indirect
	github.com/mitchellh/copystructure v1.0.0 // indirect
	github.com/mitchellh/mapstructure v1.3.3
	github.com/mojocn/base64Captcha v1.3.4
	github.com/nats-io/go-nats v1.7.2
	github.com/nats-io/nats-server/v2 v2.1.6
	github.com/nats-io/nats-streaming-server v0.17.0
	github.com/nats-io/not.go v0.0.0-20190215212113-f31ff89f78fd // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/opentracing/opentracing-go v1.1.0 // indirect
	github.com/outbrain/golib v0.0.0-20180830062331-ab954725f502
	github.com/pingcap/dm v0.0.0-00010101000000-000000000000
	github.com/pingcap/parser v3.0.11+incompatible
	github.com/pingcap/tidb v0.0.0-20190613131440-c59a108c28b7
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.4.0
	github.com/remyoudompheng/bigfft v0.0.0-20190728182440-6a916e37a237 // indirect
	github.com/satori/go.uuid v1.2.0
	github.com/shirou/gopsutil v2.20.2+incompatible
	github.com/siddontang/go v0.0.0-20180604090527-bdc77568d726
	github.com/siddontang/go-mysql v0.0.0-20200311002057-7a62847fcdb5
	github.com/stretchr/testify v1.6.1
	github.com/swaggo/echo-swagger v1.1.0
	github.com/swaggo/swag v1.7.0
	github.com/ugorji/go v1.1.7 // indirect
	golang.org/x/net v0.0.0-20201224014010-6772e930b67b
	golang.org/x/text v0.3.5
	google.golang.org/grpc v1.28.0 // indirect
)

replace github.com/siddontang/go-mysql => github.com/ffffwh/go-mysql v0.0.0-20201125093656-67dc9957b4da

//replace github.com/Sirupsen/logrus => github.com/sirupsen/logrus v1.4.2

replace github.com/araddon/qlbridge => github.com/ffffwh/qlbridge v0.0.0-20181026023605-fc2d5205dad3

replace github.com/pingcap/dm => github.com/actiontech/dm v0.0.0-20201207121004-64b8d8152b3b

replace github.com/pingcap/parser => github.com/actiontech/parser v0.0.0-20210519114308-d17de82967b8

replace github.com/pingcap/tidb => github.com/actiontech/tidb v0.0.0-20190910052938-5df091000377
