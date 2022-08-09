package main

import (
	"fmt"
	nFormatter "github.com/antonfisher/nested-logrus-formatter"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"
	"github.com/valyala/fastjson"
	"golang.org/x/sync/semaphore"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
	"os"
	"runtime"
	"strings"
	"time"
)

func init() {
	viper.AutomaticEnv()
	viper.AllowEmptyEnv(true)
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	err := viper.ReadInConfig()
	if err != nil {
		logrus.Panic(err)
		os.Exit(-1)
	}

	logrus.SetLevel(logrus.InfoLevel)
	logrus.SetOutput(os.Stdout)
	logrus.SetReportCaller(true)
	logrus.SetFormatter(&nFormatter.Formatter{
		NoColors:        false,
		HideKeys:        false,
		TimestampFormat: time.Stamp,
		CallerFirst:     true,
		CustomCallerFormatter: func(frame *runtime.Frame) string {
			filename := ""
			slash := strings.LastIndex(frame.File, "/")
			if slash >= 0 {
				filename = frame.File[slash+1:]
			}
			return fmt.Sprintf("「%s:%d」", filename, frame.Line)
		},
	})
}

type maintainer struct {
	maxConcurrency *semaphore.Weighted
	eachFetchNum   int
	client         *fasthttp.Client
	pxierBaseUrl   string
	checkUrl       string
	writeDB        *gorm.DB
	maxErr         int
}

type proxy struct {
	Id        int    `gorm:"primaryKey; autoIncrement" json:"id"`
	Address   string `json:"address"`
	Provider  string `json:"provider"`
	CreatedAt int64  `json:"-"`
	UpdatedAt int64  `json:"-"`
	ErrTimes  int    `json:"-"`
	DialType  string `json:"dial_type"`
}

func (p *proxy) TableName() string {
	return "proxy"
}

func main() {
	maxConcurrency := viper.GetInt64("max_concurrency")
	if maxConcurrency == 0 {
		logrus.Warn("missing max_concurrency, set to 10")
		maxConcurrency = 10
	}
	eachFetchNum := viper.GetInt("each_fetch_num")
	if eachFetchNum == 0 {
		logrus.Warn("missing each_fetch_num, set to 10")
		eachFetchNum = 10
	}
	baseUrl := viper.GetString("pxier_base_url")
	if len(baseUrl) == 0 {
		logrus.Panic("missing pxier_base_url")
	}
	checkUrl := viper.GetString("check_connection_url")
	if len(checkUrl) == 0 {
		logrus.Panic("missing check_connection_url")
	}
	maxErr := viper.GetInt("max_err")
	if maxErr == 0 {
		maxErr = 5
	}
	m := &maintainer{
		maxConcurrency: semaphore.NewWeighted(maxConcurrency),
		eachFetchNum:   eachFetchNum,
		client:         &fasthttp.Client{MaxConnsPerHost: int(maxConcurrency * 2)},
		pxierBaseUrl:   baseUrl,
		checkUrl:       checkUrl,
		writeDB:        newWriteDB(),
		maxErr:         maxErr,
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			logrus.Info("remove dead proxy")
			m.removeDeadProxy()
			<-ticker.C
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		logrus.Info("maintain")
		m.fetch()
		<-ticker.C
	}
}

func newWriteDB() *gorm.DB {
	logrus.Info("start write mysql")
	url := viper.GetString("write_db")
	if len(url) == 0 {
		logrus.Panic("mysql url is empty")
	}
	db, err := gorm.Open(mysql.Open(url), &gorm.Config{
		Logger: gormLogger.Default.LogMode(gormLogger.Silent),
	})
	if err != nil {
		logrus.WithError(err).Panic("failed to create db")
	}
	if err := db.AutoMigrate(&proxy{}); err != nil {
		logrus.WithError(err).Panic("failed to migrate model")
	}
	d, _ := db.DB()
	d.SetMaxIdleConns(10)
	d.SetMaxOpenConns(100)
	d.SetConnMaxLifetime(time.Hour)
	return db
}

func (m *maintainer) fetch() {
	logrus.Info("fetch proxy")
	req := fasthttp.AcquireRequest()
	res := fasthttp.AcquireResponse()
	defer func() {
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(res)
	}()

	req.SetRequestURI(fmt.Sprintf("%s/require?num=%d&provider=mix", m.pxierBaseUrl, m.eachFetchNum))
	req.Header.SetMethod(fasthttp.MethodGet)
	if err := m.client.DoTimeout(req, res, 15*time.Second); err != nil {
		logrus.WithError(err).Error("failed to fetch proxy")
		return
	}
	if res.StatusCode() != 200 {
		logrus.WithField("raw", string(res.Body())).Error("status code is not 200")
		return
	}
	jd, err := fastjson.ParseBytes(res.Body())
	if err != nil {
		logrus.WithError(err).WithField("raw", string(res.Body())).Error("failed to parse json body")
		return
	}
	for _, each := range jd.GetArray("data") {
		address := string(each.GetStringBytes("address"))
		dialType := string(each.GetStringBytes("dial_type"))
		provider := string(each.GetStringBytes("provider"))
		id := each.GetInt("id")
		go m.validate(address, dialType, provider, id)
	}
}

func (m *maintainer) validate(addr, dt, pvd string, id int) {
	var dial fasthttp.DialFunc
	switch dt {
	case "http":
		dial = fasthttpproxy.FasthttpHTTPDialer(addr)
	case "socks5":
		dial = fasthttpproxy.FasthttpSocksDialer(addr)
	default:
		logrus.WithFields(logrus.Fields{
			"addr":      addr,
			"dial_type": dt,
		}).Error("unknown dial type, return")
		return
	}
	client := &fasthttp.Client{Dial: dial}
	_, _, err := client.GetTimeout(nil, m.checkUrl, 15*time.Second)
	if err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"addr":      addr,
			"dial_type": dt,
		}).Debug("proxy error")
		m.report(pvd, id)
		return
	}
	logrus.WithFields(logrus.Fields{
		"address":   addr,
		"dial_type": dt,
	}).Info("Validated")
}

func (m *maintainer) report(pvd string, id int) {
	for {
		if m.maxConcurrency.TryAcquire(1) {
			break
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	req := fasthttp.AcquireRequest()
	res := fasthttp.AcquireResponse()
	defer func() {
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(res)
		m.maxConcurrency.Release(1)
	}()

	req.SetRequestURI(fmt.Sprintf("%s/report?id=%d&provider=%s", m.pxierBaseUrl, id, pvd))
	req.Header.SetMethod(fasthttp.MethodGet)

	if err := m.client.DoTimeout(req, res, 15*time.Second); err != nil {
		logrus.WithError(err).Warn("failed to send report http request")
	}
}

func (m *maintainer) removeDeadProxy() {
	m.writeDB.Where("err_times >= ?", m.maxErr).Delete(&proxy{})
}
