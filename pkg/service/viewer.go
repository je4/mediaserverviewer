package service

import (
	"context"
	"fmt"
	"github.com/Masterminds/sprig/v3"
	"github.com/je4/filesystem/v3/pkg/writefs"
	generic "github.com/je4/genericproto/v2/pkg/generic/proto"
	"github.com/je4/mediaserveraction/v2/pkg/actionCache"
	"github.com/je4/mediaserveraction/v2/pkg/actionController"
	mediaserverproto "github.com/je4/mediaserverproto/v2/pkg/mediaserver/proto"
	"github.com/je4/utils/v2/pkg/zLogger"
	"golang.org/x/exp/maps"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"html/template"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

type ViewerDefinition struct {
	Type        string
	Subtype     string
	name        string
	params      []string
	concurrency uint32
}

type ViewerDefinitions map[string]*ViewerDefinition

func (vd ViewerDefinitions) StringList() map[string]map[string]*generic.StringList {
	result := map[string]map[string]*generic.StringList{}
	for _, v := range vd {
		if _, ok := result[v.Type]; !ok {
			result[v.Type] = map[string]*generic.StringList{}
		}
		result[v.Type][v.name] = &generic.StringList{
			Values: v.params,
		}
	}
	return result
}

func (vd ViewerDefinitions) StringListByType(t string) map[string]*generic.StringList {
	result := map[string]*generic.StringList{}
	for _, v := range vd {
		if v.Type == t {
			result[v.name] = &generic.StringList{
				Values: v.params,
			}
		}
	}
	return result
}

var Definitions = ViewerDefinitions{
	"iiifzoomviewer": {
		Type:        "image",
		Subtype:     "",
		name:        "iiifzoomviewer",
		params:      []string{},
		concurrency: 3,
	},
	"videoviewer": {
		Type:        "video",
		Subtype:     "",
		name:        "videoviewer",
		params:      []string{"shots"},
		concurrency: 3,
	},
	"audioviewer": {
		Type:        "audio",
		Subtype:     "",
		name:        "audioviewer",
		params:      []string{"video"},
		concurrency: 3,
	},
}

/*
var ImageViewerType = "image"
var ImageViewerParams = map[string][]string{
	"iiifzoomviewer": {},
}

var VideoViewerType = "video"
var VideoViewerParams = map[string][]string{
	"videoviewer": {},
}
*/

var templateFuncs = template.FuncMap{
	"toHTML":     func(str string) template.HTML { return template.HTML(str) },
	"toHTMLAttr": func(str string) template.HTMLAttr { return template.HTMLAttr(str) },
	"toJS":       func(str string) template.JS { return template.JS(str) },
	"toURL":      func(str string) template.URL { return template.URL(str) },
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
		templates:              map[string]*template.Template{},
		definitions:            Definitions.StringList(),
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
	templates              map[string]*template.Template
	definitions            map[string]map[string]*generic.StringList
}

func (iva *viewerAction) Start() error {
	go func() {
		for {
			waitDuration := iva.refreshErrorTimeout
			for t, defs := range iva.definitions {
				if resp, err := iva.actionDispatcherClient.AddController(context.Background(), &mediaserverproto.ActionDispatcherParam{
					Type:        t,
					Actions:     defs,
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
	for t, defs := range iva.definitions {
		if resp, err := iva.actionDispatcherClient.RemoveController(context.Background(), &mediaserverproto.ActionDispatcherParam{
			Type:        t,
			Actions:     defs,
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
	var ok bool
	typeActions, ok := iva.definitions[param.GetType()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "type %s not found", param.GetType())
	}
	result, ok := typeActions[param.GetAction()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "action %s::%s not found", param.GetType(), param.GetAction())
	}
	return result, nil
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
	if item.GetMetadata().GetType() == "audio" {
		switch strings.ToLower(action) {
		case "audioviewer":
			return iva.audioViewer(item, cacheItem, storage, ap.GetParams())
		default:
			return nil, status.Errorf(codes.InvalidArgument, "no action defined")
		}
	}
	return nil, status.Errorf(codes.InvalidArgument, "type %s not supported", item.GetMetadata().GetType())
}

func (iva *viewerAction) videoViewer(item *mediaserverproto.Item, cacheItem *mediaserverproto.Cache, storage *mediaserverproto.Storage, params actionCache.ActionParams) (*mediaserverproto.Cache, error) {
	pID := fmt.Sprintf("%s/%s", "videoviewer", params.String())
	tpl, ok := iva.templates[pID]
	if !ok {
		maps.Copy(templateFuncs, sprig.FuncMap())
		tmpl, err := template.New(pID).Funcs(templateFuncs).Parse(videoViewer)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "cannot parse videoviewer template: %v", err)
		}
		iva.templates[pID] = tmpl
		tpl = tmpl
	}
	var str = strings.Builder{}
	shotsStr := params.Get("shots")
	shots, err := strconv.Atoi(shotsStr)
	if err != nil {
		shots = 0
	}
	if err := tpl.Execute(&str,
		struct {
			Shots    int
			Duration int64
		}{
			Shots:    shots,
			Duration: cacheItem.GetMetadata().GetDuration(),
		},
	); err != nil {
		return nil, status.Errorf(codes.Internal, "cannot execute videoviewer template: %v", err)
	}
	return &mediaserverproto.Cache{
		Identifier: nil, // valid for all items
		Metadata: &mediaserverproto.CacheMetadata{
			Action:   "videoviewer",
			Params:   params.String(),
			Width:    0,
			Height:   0,
			Duration: 0,
			Size:     int64(len(str.String())),
			MimeType: "text/html",
			Path:     "data:text/gohtml," + str.String(),
			Storage:  nil,
		},
	}, nil
}

func (iva *viewerAction) audioViewer(item *mediaserverproto.Item, cacheItem *mediaserverproto.Cache, storage *mediaserverproto.Storage, params actionCache.ActionParams) (*mediaserverproto.Cache, error) {
	pID := fmt.Sprintf("%s/%s", "audioviewer", params.String())
	tpl, ok := iva.templates[pID]
	if !ok {
		maps.Copy(templateFuncs, sprig.FuncMap())
		tmpl, err := template.New(pID).Funcs(templateFuncs).Parse(audioViewer)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "cannot parse audioviewer template: %v", err)
		}
		iva.templates[pID] = tmpl
		tpl = tmpl
	}
	var str = strings.Builder{}
	if err := tpl.Execute(&str,
		struct {
			Duration int64
			Video    bool
		}{
			Duration: cacheItem.GetMetadata().GetDuration(),
			Video:    params.Has("video"),
		},
	); err != nil {
		return nil, status.Errorf(codes.Internal, "cannot execute videoviewer template: %v", err)
	}
	return &mediaserverproto.Cache{
		Identifier: nil, // valid for all items
		Metadata: &mediaserverproto.CacheMetadata{
			Action:   "audioviewer",
			Params:   params.String(),
			Width:    0,
			Height:   0,
			Duration: 0,
			Size:     int64(len(str.String())),
			MimeType: "text/html",
			Path:     "data:text/gohtml," + str.String(),
			Storage:  nil,
		},
	}, nil
}

func (iva *viewerAction) iiifZoomViewer(item *mediaserverproto.Item, cacheItem *mediaserverproto.Cache, storage *mediaserverproto.Storage, params actionCache.ActionParams) (*mediaserverproto.Cache, error) {
	pID := fmt.Sprintf("%s/%s", "iiifzoomviewer", params.String())
	tpl, ok := iva.templates[pID]
	if !ok {
		tmpl, err := template.New(pID).Funcs(templateFuncs).Parse(iiifZoomViewer)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "cannot parse videoviewer template: %v", err)
		}
		iva.templates[pID] = tmpl
		tpl = tmpl
	}
	var str = strings.Builder{}
	if err := tpl.Execute(&str, params); err != nil {
		return nil, status.Errorf(codes.Internal, "cannot execute iiifZoomViewer template: %v", err)
	}
	return &mediaserverproto.Cache{
		Identifier: nil, // valid for all items
		Metadata: &mediaserverproto.CacheMetadata{
			Action:   "iiifzoomviewer",
			Params:   params.String(),
			Width:    0,
			Height:   0,
			Duration: 0,
			Size:     int64(len(str.String())),
			MimeType: "text/html",
			Path:     "data:text/gohtml," + str.String(),
			Storage:  nil,
		},
	}, nil
}
