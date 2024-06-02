package service

import (
	"context"
	"fmt"
	"github.com/je4/filesystem/v3/pkg/writefs"
	generic "github.com/je4/genericproto/v2/pkg/generic/proto"
	"github.com/je4/mediaserveraction/v2/pkg/actionCache"
	"github.com/je4/mediaserveraction/v2/pkg/actionController"
	"github.com/je4/mediaserverimage/v2/pkg/image"
	mediaserverproto "github.com/je4/mediaserverproto/v2/pkg/mediaserver/proto"
	"github.com/je4/utils/v2/pkg/zLogger"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/vp8"
	_ "golang.org/x/image/vp8l"
	_ "golang.org/x/image/webp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"io/fs"
	"regexp"
	"strings"
	"time"
)

var ImageViewerType = "image"
var ImageViewerParams = map[string][]string{
	"iiifzoomviewer": {},
}
-
func NewImageViewerAction(adClient mediaserverproto.ActionDispatcherClient, host string, port uint32, concurrency uint32, refreshErrorTimeout time.Duration, vfs fs.FS, db mediaserverproto.DatabaseClient, logger zLogger.ZLogger) (*imageViewerAction, error) {
	_logger := logger.With().Str("rpcService", "imageViewerAction").Logger()
	return &imageViewerAction{
		actionDispatcherClient: adClient,
		done:                   make(chan bool),
		host:                   host,
		port:                   port,
		refreshErrorTimeout:    refreshErrorTimeout,
		vFS:                    vfs,
		db:                     db,
		logger:                 &_logger,
		image:                  image.NewImageHandler(logger),
		concurrency:            concurrency,
	}, nil
}

type imageViewerAction struct {
	mediaserverproto.UnimplementedActionServer
	actionDispatcherClient mediaserverproto.ActionDispatcherClient
	logger                 zLogger.ZLogger
	done                   chan bool
	host                   string
	port                   uint32
	refreshErrorTimeout    time.Duration
	vFS                    fs.FS
	db                     mediaserverproto.DatabaseClient
	image                  image.ImageHandler
	concurrency            uint32
}

func (ia *imageViewerAction) Start() error {
	actionParams := map[string]*generic.StringList{}
	for action, params := range ImageViewerParams {
		actionParams[action] = &generic.StringList{
			Values: params,
		}
	}
	go func() {
		for {
			waitDuration := ia.refreshErrorTimeout
			if resp, err := ia.actionDispatcherClient.AddController(context.Background(), &mediaserverproto.ActionDispatcherParam{
				Type:        ImageViewerType,
				Actions:     actionParams,
				Host:        &ia.host,
				Port:        ia.port,
				Concurrency: ia.concurrency,
			}); err != nil {
				ia.logger.Error().Err(err).Msg("cannot add controller")
			} else {
				if resp.GetResponse().GetStatus() != generic.ResultStatus_OK {
					ia.logger.Error().Err(err).Msgf("cannot add controller: %s", resp.GetResponse().GetMessage())
				} else {
					waitDuration = time.Duration(resp.GetNextCallWait()) * time.Second
					ia.logger.Info().Msgf("controller %s:%d added", ia.host, ia.port)
				}
			}
			select {
			case <-time.After(waitDuration):
				continue
			case <-ia.done:
				return
			}
		}
	}()
	return nil
}

func (ia *imageViewerAction) GracefulStop() {
	if err := ia.image.Close(); err != nil {
		ia.logger.Error().Err(err).Msg("cannot close image handler")
	}
	actionParams := map[string]*generic.StringList{}
	for action, params := range ImageViewerParams {
		actionParams[action] = &generic.StringList{
			Values: params,
		}
	}
	if resp, err := ia.actionDispatcherClient.RemoveController(context.Background(), &mediaserverproto.ActionDispatcherParam{
		Type:        ImageViewerType,
		Actions:     actionParams,
		Host:        &ia.host,
		Port:        ia.port,
		Concurrency: ia.concurrency,
	}); err != nil {
		ia.logger.Error().Err(err).Msg("cannot remove controller")
	} else {
		if resp.GetStatus() != generic.ResultStatus_OK {
			ia.logger.Error().Err(err).Msgf("cannot remove controller: %s", resp.GetMessage())
		} else {
			ia.logger.Info().Msgf("controller %s:%d removed", ia.host, ia.port)
		}

	}
	ia.done <- true
}

func (ia *imageViewerAction) Ping(context.Context, *emptypb.Empty) (*generic.DefaultResponse, error) {
	return &generic.DefaultResponse{
		Status:  generic.ResultStatus_OK,
		Message: "pong",
		Data:    nil,
	}, nil
}

func (ia *imageViewerAction) GetParams(ctx context.Context, param *mediaserverproto.ParamsParam) (*generic.StringList, error) {
	params, ok := ImageViewerParams[param.GetAction()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "action %s::%s not found", param.GetType(), param.GetAction())
	}
	return &generic.StringList{
		Values: params,
	}, nil
}


func (ia *imageViewerAction) Action(ctx context.Context, ap *mediaserverproto.ActionParam) (*mediaserverproto.Cache, error) {
	item := ap.GetItem()
	if item == nil {
		return nil, status.Errorf(codes.InvalidArgument, "no item defined")
	}
	itemIdentifier := item.GetIdentifier()
	storage := ap.GetStorage()
	if storage == nil {
		return nil, status.Errorf(codes.InvalidArgument, "no storage defined")
	}
	cacheItem, err := ia.db.GetCache(context.Background(), &mediaserverproto.CacheRequest{
		Identifier: itemIdentifier,
		Action:     "item",
		Params:     "",
	})
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "cannot get cache %s/%s/item: %v", itemIdentifier.GetCollection(), itemIdentifier.GetSignature(), err)
	}
	action := ap.GetAction()
	switch strings.ToLower(action) {
	case "iiifzoomviewer":
		return ia.iiifZoomViewer(item, cacheItem, storage, ap.GetParams())
	default:
		return nil, status.Errorf(codes.InvalidArgument, "no action defined")

	}
}

func (ia *imageViewerAction) iiifZoomViewer(item *mediaserverproto.Item, cacheItem *mediaserverproto.Cache, storage *mediaserverproto.Storage, params map[string]string) (*mediaserverproto.Cache, error) {
	info := fmt.Sprintf("iiif/%s/%s/info.json", item.GetIdentifier().GetCollection(), item.GetIdentifier().GetSignature())

}
