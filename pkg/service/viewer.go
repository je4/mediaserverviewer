package service

import (
	"context"
	"fmt"
	"github.com/je4/filesystem/v3/pkg/writefs"
	generic "github.com/je4/genericproto/v2/pkg/generic/proto"
	"github.com/je4/mediaserveraction/v2/pkg/actionCache"
	"github.com/je4/mediaserveraction/v2/pkg/actionController"
	mediaserverproto "github.com/je4/mediaserverproto/v2/pkg/mediaserver/proto"
	"github.com/je4/utils/v2/pkg/zLogger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"io"
	"io/fs"
	"strings"
	"time"
)

var ImageViewerType = "image"
var ImageViewerParams = map[string][]string{
	"iiifzoomviewer": {},
}

var VideoViewerType = "video"
var VideoViewerParams = map[string][]string{
	"videoviewer": {},
}

func NewViewerAction(adClient mediaserverproto.ActionDispatcherClient, host string, port uint32, concurrency uint32, refreshErrorTimeout time.Duration, vfs fs.FS, db mediaserverproto.DatabaseClient, iiif string, logger zLogger.ZLogger) (*viewerAction, error) {
	_logger := logger.With().Str("rpcService", "viewerAction").Logger()
	return &viewerAction{
		iiif:                   iiif,
		actionDispatcherClient: adClient,
		done:                   make(chan bool),
		host:                   host,
		port:                   port,
		refreshErrorTimeout:    refreshErrorTimeout,
		vFS:                    vfs,
		db:                     db,
		logger:                 &_logger,
		concurrency:            concurrency,
	}, nil
}

type viewerAction struct {
	mediaserverproto.UnimplementedActionServer
	actionDispatcherClient mediaserverproto.ActionDispatcherClient
	logger                 zLogger.ZLogger
	done                   chan bool
	host                   string
	port                   uint32
	refreshErrorTimeout    time.Duration
	vFS                    fs.FS
	db                     mediaserverproto.DatabaseClient
	concurrency            uint32
	iiif                   string
}

func (iva *viewerAction) Start() error {
	imageActionParams := map[string]*generic.StringList{}
	for action, params := range ImageViewerParams {
		imageActionParams[action] = &generic.StringList{
			Values: params,
		}
	}
	videoActionParams := map[string]*generic.StringList{}
	for action, params := range VideoViewerParams {
		videoActionParams[action] = &generic.StringList{
			Values: params,
		}
	}
	go func() {
		for {
			waitDuration := iva.refreshErrorTimeout
			if resp, err := iva.actionDispatcherClient.AddController(context.Background(), &mediaserverproto.ActionDispatcherParam{
				Type:        ImageViewerType,
				Actions:     imageActionParams,
				Host:        &iva.host,
				Port:        iva.port,
				Concurrency: iva.concurrency,
			}); err != nil {
				iva.logger.Error().Err(err).Msg("cannot add controller")
			} else {
				if resp.GetResponse().GetStatus() != generic.ResultStatus_OK {
					iva.logger.Error().Err(err).Msgf("cannot add controller: %s", resp.GetResponse().GetMessage())
				} else {
					waitDuration = time.Duration(resp.GetNextCallWait()) * time.Second
					iva.logger.Info().Msgf("controller %s:%d added", iva.host, iva.port)
				}
			}
			if resp, err := iva.actionDispatcherClient.AddController(context.Background(), &mediaserverproto.ActionDispatcherParam{
				Type:        VideoViewerType,
				Actions:     videoActionParams,
				Host:        &iva.host,
				Port:        iva.port,
				Concurrency: iva.concurrency,
			}); err != nil {
				iva.logger.Error().Err(err).Msg("cannot add controller")
			} else {
				if resp.GetResponse().GetStatus() != generic.ResultStatus_OK {
					iva.logger.Error().Err(err).Msgf("cannot add controller: %s", resp.GetResponse().GetMessage())
				} else {
					waitDuration = time.Duration(resp.GetNextCallWait()) * time.Second
					iva.logger.Info().Msgf("controller %s:%d added", iva.host, iva.port)
				}
			}
			select {
			case <-time.After(waitDuration):
				continue
			case <-iva.done:
				return
			}
		}
	}()
	return nil
}

func (iva *viewerAction) GracefulStop() {
	imageActionParams := map[string]*generic.StringList{}
	for action, params := range ImageViewerParams {
		imageActionParams[action] = &generic.StringList{
			Values: params,
		}
	}
	if resp, err := iva.actionDispatcherClient.RemoveController(context.Background(), &mediaserverproto.ActionDispatcherParam{
		Type:        ImageViewerType,
		Actions:     imageActionParams,
		Host:        &iva.host,
		Port:        iva.port,
		Concurrency: iva.concurrency,
	}); err != nil {
		iva.logger.Error().Err(err).Msg("cannot remove controller")
	} else {
		if resp.GetStatus() != generic.ResultStatus_OK {
			iva.logger.Error().Err(err).Msgf("cannot remove controller: %s", resp.GetMessage())
		} else {
			iva.logger.Info().Msgf("controller %s:%d removed", iva.host, iva.port)
		}

	}
	actionParams := map[string]*generic.StringList{}
	for action, params := range VideoViewerParams {
		actionParams[action] = &generic.StringList{
			Values: params,
		}
	}
	if resp, err := iva.actionDispatcherClient.RemoveController(context.Background(), &mediaserverproto.ActionDispatcherParam{
		Type:        VideoViewerType,
		Actions:     actionParams,
		Host:        &iva.host,
		Port:        iva.port,
		Concurrency: iva.concurrency,
	}); err != nil {
		iva.logger.Error().Err(err).Msg("cannot remove controller")
	} else {
		if resp.GetStatus() != generic.ResultStatus_OK {
			iva.logger.Error().Err(err).Msgf("cannot remove controller: %s", resp.GetMessage())
		} else {
			iva.logger.Info().Msgf("controller %s:%d removed", iva.host, iva.port)
		}

	}
	iva.done <- true
}

func (iva *viewerAction) Ping(context.Context, *emptypb.Empty) (*generic.DefaultResponse, error) {
	return &generic.DefaultResponse{
		Status:  generic.ResultStatus_OK,
		Message: "pong",
		Data:    nil,
	}, nil
}

func (iva *viewerAction) GetParams(ctx context.Context, param *mediaserverproto.ParamsParam) (*generic.StringList, error) {
	var params []string
	var ok bool
	switch param.GetType() {
	case ImageViewerType:
		params, ok = ImageViewerParams[param.GetAction()]
	case VideoViewerType:
		params, ok = VideoViewerParams[param.GetAction()]
	default:
		return nil, status.Errorf(codes.NotFound, "type %s not found", param.GetType())
	}
	if !ok {
		return nil, status.Errorf(codes.NotFound, "action %s::%s not found", param.GetType(), param.GetAction())
	}
	return &generic.StringList{
		Values: params,
	}, nil
}

func (iva *viewerAction) storeString(str, mime string, action string, item *mediaserverproto.Item, itemCache *mediaserverproto.Cache, storage *mediaserverproto.Storage, params actionCache.ActionParams, format string) (*mediaserverproto.Cache, error) {
	itemIdentifier := item.GetIdentifier()
	cacheName := actionController.CreateCacheName(itemIdentifier.GetCollection(), itemIdentifier.GetSignature(), action, params.String(), format)
	targetPath := fmt.Sprintf(
		"%s/%s/%s",
		storage.GetFilebase(),
		storage.GetDatadir(),
		cacheName)
	target, err := writefs.Create(iva.vFS, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "cannot open %s: %v", targetPath, err)
	}
	defer target.Close()
	l, err := io.WriteString(target, str)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cannot write to %s: %v", targetPath, err)
	}
	resp := &mediaserverproto.Cache{
		Identifier: &mediaserverproto.ItemIdentifier{
			Collection: itemIdentifier.GetCollection(),
			Signature:  itemIdentifier.GetSignature(),
		},
		Metadata: &mediaserverproto.CacheMetadata{
			Action:   action,
			Params:   params.String(),
			Width:    0,
			Height:   0,
			Duration: 0,
			Size:     int64(l),
			MimeType: mime,
			Path:     fmt.Sprintf("%s/%s", storage.GetDatadir(), cacheName),
			Storage:  storage,
		},
	}
	return resp, nil

}

func (iva *viewerAction) Action(ctx context.Context, ap *mediaserverproto.ActionParam) (*mediaserverproto.Cache, error) {
	item := ap.GetItem()
	if item == nil {
		return nil, status.Errorf(codes.InvalidArgument, "no item defined")
	}
	itemIdentifier := item.GetIdentifier()
	storage := ap.GetStorage()
	if storage == nil {
		return nil, status.Errorf(codes.InvalidArgument, "no storage defined")
	}
	cacheItem, err := iva.db.GetCache(context.Background(), &mediaserverproto.CacheRequest{
		Identifier: itemIdentifier,
		Action:     "item",
		Params:     "",
	})
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "cannot get cache %s/%s/item: %v", itemIdentifier.GetCollection(), itemIdentifier.GetSignature(), err)
	}
	action := ap.GetAction()
	if item.GetMetadata().GetType() == "image" {
		switch strings.ToLower(action) {
		case "iiifzoomviewer":
			return iva.iiifZoomViewer(item, cacheItem, storage, ap.GetParams())
		default:
			return nil, status.Errorf(codes.InvalidArgument, "no action defined")
		}
	}
	if item.GetMetadata().GetType() == "video" {
		switch strings.ToLower(action) {
		case "videoviewer":
			return iva.videoViewer(item, cacheItem, storage, ap.GetParams())
		default:
			return nil, status.Errorf(codes.InvalidArgument, "no action defined")
		}
	}
	return nil, status.Errorf(codes.InvalidArgument, "type %s not supported", item.GetMetadata().GetType())
}

func (iva *viewerAction) videoViewer(item *mediaserverproto.Item, cacheItem *mediaserverproto.Cache, storage *mediaserverproto.Storage, params map[string]string) (*mediaserverproto.Cache, error) {
	var replacements = map[string]string{
		"<<collection>>": item.GetIdentifier().GetCollection(),
		"<<signature>>":  item.GetIdentifier().GetSignature(),
		"<<info>>":       fmt.Sprintf("iiif/3/%s/%s/info.json", item.GetIdentifier().GetCollection(), item.GetIdentifier().GetSignature()),
	}
	str := videoViewer
	for key, value := range replacements {
		str = strings.ReplaceAll(str, key, value)
	}
	return iva.storeString(str, "text/gohtml", "iiifzoomviewer", item, cacheItem, storage, params, "gohtml")
}

func (iva *viewerAction) iiifZoomViewer(item *mediaserverproto.Item, cacheItem *mediaserverproto.Cache, storage *mediaserverproto.Storage, params map[string]string) (*mediaserverproto.Cache, error) {
	var replacements = map[string]string{
		"<<collection>>": item.GetIdentifier().GetCollection(),
		"<<signature>>":  item.GetIdentifier().GetSignature(),
		"<<info>>":       fmt.Sprintf("iiif/3/%s/%s/info.json", item.GetIdentifier().GetCollection(), item.GetIdentifier().GetSignature()),
	}
	str := iiifZoomViewer
	for key, value := range replacements {
		str = strings.ReplaceAll(str, key, value)
	}
	return iva.storeString(str, "text/gohtml", "iiifzoomviewer", item, cacheItem, storage, params, "gohtml")
}
