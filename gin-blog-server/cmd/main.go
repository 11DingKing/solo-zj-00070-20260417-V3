package main

import (
	"flag"
	ginblog "gin-blog/internal"
	g "gin-blog/internal/global"
	"gin-blog/internal/middleware"
	"log"
	"strings"

	"github.com/gin-gonic/gin"
)

func main() {
	configPath := flag.String("c", "../config.yml", "配置文件路径")
	flag.Parse()

	conf := g.ReadConfig(*configPath)

	_ = ginblog.InitLogger(conf)
	db := ginblog.InitDatabase(conf)
	rdb := ginblog.InitRedis(conf)

	handle.StartViewCountSyncJob(rdb, db)

	gin.SetMode(conf.Server.Mode)
	r := gin.New()
	r.SetTrustedProxies([]string{"*"})
	if conf.Server.Mode == "debug" {
		r.Use(gin.Logger(), gin.Recovery())
	} else {
		r.Use(middleware.Recovery(true), middleware.Logger())
	}
	r.Use(middleware.CORS())
	r.Use(middleware.WithGormDB(db))
	r.Use(middleware.WithRedisDB(rdb))
	r.Use(middleware.WithCookieStore(conf.Session.Name, conf.Session.Salt))
	ginblog.RegisterHandlers(r)

	if conf.Upload.OssType == "local" {
		r.Static(conf.Upload.Path, conf.Upload.StorePath)
	}

	serverAddr := conf.Server.Port
	if serverAddr[0] == ':' || strings.HasPrefix(serverAddr, "0.0.0.0:") {
		log.Printf("Serving HTTP on (http://localhost:%s/) ... \n", strings.Split(serverAddr, ":")[1])
	} else {
		log.Printf("Serving HTTP on (http://%s/) ... \n", serverAddr)
	}
	r.Run(serverAddr)
}
