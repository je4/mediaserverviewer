package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"github.com/je4/certloader/v2/pkg/loader"
	"github.com/je4/filesystem/v3/pkg/vfsrw"
	mediaserverproto "github.com/je4/mediaserverproto/v2/pkg/mediaserver/proto"
	"github.com/je4/mediaserverviewer/v2/configs"
	"github.com/je4/mediaserverviewer/v2/pkg/service"
	"github.com/je4/miniresolver/v2/pkg/resolver"
	"github.com/je4/utils/v2/pkg/zLogger"
	ublogger "gitlab.switch.ch/ub-unibas/go-ublogger"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var cfg = flag.String("config", "", "location of toml configuration file")

func main() {
	flag.Parse()
	var cfgFS fs.FS
	var cfgFile string
	if *cfg != "" {
		cfgFS = os.DirFS(filepath.Dir(*cfg))
		cfgFile = filepath.Base(*cfg)
	} else {
		cfgFS = configs.ConfigFS
		cfgFile = "mediaserverviewer.toml"
	}
	conf := &MediaserverImageConfig{
		LocalAddr:   "localhost:8443",
		Concurrency: 3,
	}
	if err := LoadMediaserverImageConfig(cfgFS, cfgFile, conf); err != nil {
		log.Fatalf("cannot load toml from [%v] %s: %v", cfgFS, cfgFile, err)
	}

	// create logger instance
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatalf("cannot get hostname: %v", err)
	}

	instance := "instance_" + conf.Instance

	var loggerTLSConfig *tls.Config
	var loggerLoader io.Closer
	if conf.Log.Stash.TLS != nil {
		loggerTLSConfig, loggerLoader, err = loader.CreateClientLoader(conf.Log.Stash.TLS, nil)
		if err != nil {
			log.Fatalf("cannot create client loader: %v", err)
		}
		defer loggerLoader.Close()
	}

	_logger, _logstash, _logfile := ublogger.CreateUbMultiLoggerTLS(conf.Log.Level, conf.Log.File,
		ublogger.SetDataset(conf.Log.Stash.Dataset),
		ublogger.SetLogStash(conf.Log.Stash.LogstashHost, conf.Log.Stash.LogstashPort, conf.Log.Stash.Namespace, conf.Log.Stash.LogstashTraceLevel),
		ublogger.SetTLS(conf.Log.Stash.TLS != nil),
		ublogger.SetTLSConfig(loggerTLSConfig),
	)
	if _logstash != nil {
		defer _logstash.Close()
	}
	if _logfile != nil {
		defer _logfile.Close()
	}

	l2 := _logger.With().Timestamp().Str("host", hostname).Logger() //.Output(output)
	var logger zLogger.ZLogger = &l2

	vfs, err := vfsrw.NewFS(conf.VFS, logger)
	if err != nil {
		logger.Panic().Err(err).Msg("cannot create vfs")
	}
	defer func() {
		if err := vfs.Close(); err != nil {
			logger.Error().Err(err).Msg("cannot close vfs")
		}
	}()

	// create TLS Certificate.
	// the certificate MUST contain <package>.<service> as DNS name
	serverTLSConfig, serverLoader, err := loader.CreateServerLoader(true, &conf.Server, nil, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create server loader")
	}
	defer serverLoader.Close()

	// create client TLS certificate
	// the certificate MUST contain "grpc:miniresolverproto.MiniResolver" or "*" in URIs
	clientTLSConfig, clientLoader, err := loader.CreateClientLoader(&conf.Client, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create client loader")
	}
	defer clientLoader.Close()

	// create resolver client
	resolverClient, err := resolver.NewMiniresolverClient(conf.ResolverAddr, conf.GRPCClient, clientTLSConfig, serverTLSConfig, time.Duration(conf.ResolverTimeout), time.Duration(conf.ResolverNotFoundTimeout), logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create resolver client")
	}
	defer resolverClient.Close()

	// create grpc server with resolver for name resolution
	grpcServer, err := resolverClient.NewServer(conf.LocalAddr, []string{instance}, true)
	//grpcServer, err := grpchelper.NewServer(conf.LocalAddr, serverTLSConfig, nil, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create server")
	}

	addr := grpcServer.GetAddr()
	l2 = _logger.With().Str("addr", addr).Logger() //.Output(output)
	logger = &l2

	actionDispatcherClients, err := resolver.NewClients[mediaserverproto.ActionDispatcherClient](resolverClient,
		mediaserverproto.NewActionDispatcherClient,
		mediaserverproto.ActionDispatcher_ServiceDesc.ServiceName, conf.Domains)
	if err != nil {
		logger.Panic().Msgf("cannot create mediaserveractiondispatcher grpc client: %v", err)
	}
	for _, actionDispatcherClient := range actionDispatcherClients {
		resolver.DoPing(actionDispatcherClient, logger)
	}

	dbClients, err := resolver.NewClients[mediaserverproto.DatabaseClient](resolverClient, mediaserverproto.NewDatabaseClient, mediaserverproto.Database_ServiceDesc.ServiceName, conf.Domains)
	if err != nil {
		logger.Panic().Msgf("cannot create mediaserverdb grpc client: %v", err)
	}
	for _, dbClient := range dbClients {
		resolver.DoPing(dbClient, logger)
	}

	srv, err := service.NewActionService(actionDispatcherClients, conf.Instance, conf.Domains, conf.Concurrency, conf.QueueSize, time.Duration(conf.ResolverNotFoundTimeout), vfs, dbClients, conf.IIIF, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create service")
	}
	if err := srv.Start(); err != nil {
		logger.Fatal().Err(err).Msg("cannot start service")
	}
	defer srv.GracefulStop()

	// register the server
	mediaserverproto.RegisterActionServer(grpcServer, srv)

	grpcServer.Startup()

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)
	fmt.Println("press ctrl+c to stop server")
	s := <-done
	fmt.Println("got signal:", s)

	defer grpcServer.Shutdown()
}
