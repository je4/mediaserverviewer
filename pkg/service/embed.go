package service

import _ "embed"

//go:embed iiifzoomviewer.gotmpl
var iiifZoomViewer string

//go:embed videoviewer.gotmpl
var videoViewer string

//go:embed audioviewer.gotmpl
var audioViewer string
