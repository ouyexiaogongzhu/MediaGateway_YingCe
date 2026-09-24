package service

import "infinite-canvas/backend/internal/app"

// aliases_fork 再导出我方 fork 独有（上游无对应）的类型与函数，供 handler/cmd 使用。
// Service 上的方法经 Service = app.Service 别名自动可用，无需在此声明。

type (
	RenderAllShotsRequest         = app.RenderAllShotsRequest
	RenderAllShotsResult          = app.RenderAllShotsResult
	RenderedShotResult            = app.RenderedShotResult
	StoryboardVideoBatchRequest   = app.StoryboardVideoBatchRequest
	StoryboardMusicBatchRequest   = app.StoryboardMusicBatchRequest
	StoryboardComposeRequest      = app.StoryboardComposeRequest
	StoryboardRowVideoTaskRequest = app.StoryboardRowVideoTaskRequest
)

var MediaGatewayBaseURL = app.MediaGatewayBaseURL
