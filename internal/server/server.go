package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/xuanli27/octopus/internal/conf"
	_ "github.com/xuanli27/octopus/internal/server/handlers"
	"github.com/xuanli27/octopus/internal/server/middleware"
	"github.com/xuanli27/octopus/internal/server/resp"
	"github.com/xuanli27/octopus/internal/server/router"
	"github.com/xuanli27/octopus/internal/utils/log"
	"github.com/xuanli27/octopus/internal/utils/safe"
	"github.com/xuanli27/octopus/static"
)

var httpSrv http.Server

func Start() error {
	if conf.IsDebug() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		log.Errorf("http panic recovered: %v", recovered)
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		c.Abort()
	}))

	r.Use(gzip.Gzip(gzip.DefaultCompression,
		gzip.WithExcludedPaths([]string{"/v1/"}),
		gzip.WithExcludedPathsRegexs([]string{`/api/v1/log/stream`}),
	))

	r.Use(middleware.Logger(middleware.LoggerConfig{
		Enabled:       conf.AppConfig.Log.Access.Enabled || conf.IsDebug(),
		SlowThreshold: time.Duration(conf.AppConfig.Log.Access.SlowThresholdMS) * time.Millisecond,
	}))
	r.Use(middleware.Cors())
	r.Use(middleware.StaticEmbed("/", static.StaticFS))

	router.RegisterAll(r)

	httpSrv.Addr = fmt.Sprintf("%s:%d", conf.AppConfig.Server.Host, conf.AppConfig.Server.Port)
	httpSrv.Handler = r
	safe.Go("http-listen", func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Errorf("http server listen and serve error: %v", err)
		}
	})
	return nil
}

func Close() error {
	return httpSrv.Close()
}
