package service

import _ "embed"

//go:embed iiifzoomviewer.gotmpl
var iiifZoomViewer string

//go:embed videoviewer.gotmpl
var videoViewer string

//go:embed audioviewer.gotmpl
var audioViewer string

//go:embed pdfviewer.gotmpl
var pdfViewer string

//go:embed replaywebviewer.gotmpl
var replayWebViewer string
