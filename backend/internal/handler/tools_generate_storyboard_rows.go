package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// 生成分鏰行工具：契约指令 + 劇本 → 文本模型 → 分镜行節點，后端同步执行
// （长剧本可达数分钟），限流与 /tools/upscale 同口径。
func toolsGenerateStoryboardRows(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "tools-generate-storyboard-rows:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req struct {
			CanvasID       string `json:"canvasId"`
			SourceNodeID   string `json:"sourceNodeId"`
			Model          string `json:"model"`          // 画布当前文本模型（channelId::modelKey 编码，与生成请求同构）
			LogicalModelID string `json:"logicalModelId"` // 前台模型模式的路由标识
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.CanvasID) == "" || strings.TrimSpace(req.SourceNodeID) == "" {
			fail(c, http.StatusBadRequest, errors.New("canvasId 和 sourceNodeId 必填"))
			return
		}
		result, err := svc.GenerateStoryboardRows(c.Request.Context(), user.ID, req.CanvasID, req.SourceNodeID, strings.TrimSpace(req.Model), strings.TrimSpace(req.LogicalModelID))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	}
}
