package main

import (
	"emperror.dev/errors"
	"github.com/BurntSushi/toml"
	"github.com/je4/certloader/v2/pkg/loader"
	"github.com/je4/filesystem/v3/pkg/vfsrw"
	"github.com/je4/utils/v2/pkg/config"
	"github.com/je4/utils/v2/pkg/stashconfig"
	"io/fs"
	"os"
)

type MediaserverImageConfig struct {
	LocalAddr               string                `toml:"localaddr"`
	ResolverAddr            string                `toml:"resolveraddr"`
	IIIF                    string                `toml:"iiif"`
	Domains                 []string              `toml:"domains"`
	Instance                string                `toml:"instance"`
	QueueSize               uint32                `toml:"queuesize"`
	ResolverTimeout         config.Duration       `toml:"resolvertimeout"`
	ResolverNotFoundTimeout config.Duration       `toml:"resolvernotfoundtimeout"`
	Server                  loader.Config         `toml:"server"`
	Client                  loader.Config         `toml:"client"`
	GRPCClient              map[string]string     `toml:"grpcclient"`
	VFS                     map[string]*vfsrw.VFS `toml:"vfs"`
	Concurrency             uint32
	Log                     stashconfig.Config `toml:"log"`
}

func LoadMediaserverImageConfig(fSys fs.FS, fp string, conf *MediaserverImageConfig) error {
	if _, err := fs.Stat(fSys, fp); err != nil {
		path, err := os.Getwd()
		if err != nil {
			return errors.Wrap(err, "cannot get current working directory")
		}
		fSys = os.DirFS(path)
		fp = "mediaserverviewer.toml"
	}
	data, err := fs.ReadFile(fSys, fp)
	if err != nil {
		return errors.Wrapf(err, "cannot read file [%v] %s", fSys, fp)
	}
	_, err = toml.Decode(string(data), conf)
	if err != nil {
		return errors.Wrapf(err, "error loading config file %v", fp)
	}
	return nil
}
