package http

import (
	"context"
	"errors"
	"fmt"
	gohttp "net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	container "github.com/thehyperflames/dicontainer-go"

	aggregator "github.com/hxuan190/route-engine/internal"
	"github.com/hxuan190/route-engine/internal/config"
	"github.com/hxuan190/route-engine/internal/http/httputil"
	"github.com/hxuan190/route-engine/internal/http/middlewares"
)

const (
	API_VERSION  = "v1"
	HTTP_SERVICE = "http-service"
)

type HTTPService struct {
	container.BaseDIInstance

	aggregatorSvc *aggregator.Service
	rateLimiter   *middlewares.RateLimiter
	server        *gohttp.Server
	conf          *config.GeneralConfig

	handlers []httputil.IHttpHandler
}

func (svc *HTTPService) ID() string {
	return HTTP_SERVICE
}

func (svc *HTTPService) Start() error {
	r := gin.Default()
	r.Use(gin.Recovery())

	corsConf := cors.DefaultConfig()
	corsConf.AllowAllOrigins = true
	corsConf.AllowCredentials = true
	corsConf.AddAllowHeaders("Authorization", "X-Wallet-Address", "X-Timestamp", "X-Signature")
	r.Use(cors.New(corsConf))

	r.Use(middlewares.MetricsMiddleware())
	r.Use(svc.rateLimiter.RateLimitMiddleware())

	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	r.GET("/health", func(c *gin.Context) {
		c.JSON(gohttp.StatusOK, gin.H{"status": "ok"})
	})

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	api := r.Group("api")
	pub := api.Group(API_VERSION)
	priv := api.Group(API_VERSION)

	admin := api.Group(fmt.Sprintf("%s/admin", API_VERSION))

	svc.setupHandlers(pub, priv, admin)

	svc.server = &gohttp.Server{
		Addr:    svc.conf.HTTPHost + ":" + svc.conf.HTTPPort,
		Handler: r,
	}
	log.Info().Str("host", svc.conf.HTTPHost).Str("port", svc.conf.HTTPPort).Msg("http server started")

	if err := svc.server.ListenAndServe(); err != nil && err != gohttp.ErrServerClosed {
		return err
	}

	return nil
}

func (svc *HTTPService) Configure(c container.IContainer) error {
	svc.conf = c.GetConfig(config.GENERAL_CONFIG_KEY).(*config.GeneralConfig)
	if svc.conf == nil {
		return errors.New("invalid server config")
	}

	svc.aggregatorSvc = c.Instance(aggregator.AGGREGATOR_SERVICE).(*aggregator.Service)
	svc.rateLimiter = middlewares.NewRateLimiter(10, 20)

	svc.handlers = []httputil.IHttpHandler{
		NewPoolHandler(svc.aggregatorSvc),
		NewQuoteHandler(svc.aggregatorSvc),
		NewSwapHandler(svc.aggregatorSvc),
	}
	return nil
}

func (svc *HTTPService) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := svc.server.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("failed to stop http server")
		return err
	}
	log.Info().Msg("http server stopped gracefully")
	return nil
}

func (svc *HTTPService) setupHandlers(
	rootPub *gin.RouterGroup,
	rootPriv *gin.RouterGroup,
	rootAdmin *gin.RouterGroup,
) {
	for _, h := range svc.handlers {
		pub := rootPub.Group(h.Root())
		priv := rootPriv.Group(h.Root())
		admin := rootAdmin.Group(h.Root())
		h.SetRoutes(pub, priv, admin)
	}
}
