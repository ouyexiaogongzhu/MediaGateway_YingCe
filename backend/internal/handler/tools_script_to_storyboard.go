package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// 脚本 → 分镜脚本转换工具：把脚本节点文本里的 LLM 分镜 JSON 落成同画布新分镜节点。
// model 参数预留（文本已含分镜行，暂忽略）；限流与 /tools/upscale 同口径。
func toolsScriptToStoryboard(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "tools-script-to-storyboard:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req struct {
			CanvasID     string `json:"canvasId"`
			SourceNodeID string `json:"sourceNodeId"`
			Model        string `json:"model"` // 暂忽略：行数据直接来自脚本节点文本
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.CanvasID) == "" || strings.TrimSpace(req.SourceNodeID) == "" {
			fail(c, http.StatusBadRequest, errors.New("canvasId 和 sourceNodeId 必填"))
			return
		}
		result, err := svc.ScriptToStoryboard(user.ID, req.CanvasID, req.SourceNodeID)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	}
}
